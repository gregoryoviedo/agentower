using System.Diagnostics;
using Microsoft.Win32;

namespace Agentower;

/// <summary>
/// Per-user "install" support. The app is a single self-contained exe, so
/// there is no MSI: on first launch it offers to copy itself into
/// %LOCALAPPDATA%\Programs\Agentower, drop Start Menu / Desktop shortcuts
/// and register itself to start with Windows. Everything lives in the
/// current user's profile, so no elevation is required. It also registers
/// an entry under HKCU\...\Uninstall so Windows shows it in
/// "Apps & features" with a working Uninstall button.
/// </summary>
internal static class Installer
{
    private const string RunKey = @"Software\Microsoft\Windows\CurrentVersion\Run";
    private const string UninstallKey = @"Software\Microsoft\Windows\CurrentVersion\Uninstall\Agentower";

    public static string InstallDirectory { get; } = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "Programs", "Agentower");

    public static string InstalledExePath => Path.Combine(InstallDirectory, "Agentower.exe");

    public static string CurrentExePath =>
        Environment.ProcessPath ?? System.Windows.Forms.Application.ExecutablePath;

    public static bool IsInstalled =>
        string.Equals(Normalize(CurrentExePath), Normalize(InstalledExePath), StringComparison.OrdinalIgnoreCase);

    public static bool InstalledCopyExists => File.Exists(InstalledExePath);

    public static string PortableFlagPath => Path.Combine(AppPaths.LocalDirectory, "portable.flag");

    public static bool PortableMode => File.Exists(PortableFlagPath);

    public static string Version =>
        (typeof(Installer).Assembly.GetName().Version ?? new Version(0, 0, 0)).ToString(3);

    private static string StartMenuShortcutPath => Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.Programs), "Agentower.lnk");

    private static string DesktopShortcutPath => Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory), "Agentower.lnk");

    public static void MarkPortable()
    {
        Directory.CreateDirectory(AppPaths.LocalDirectory);
        File.WriteAllText(PortableFlagPath, DateTimeOffset.Now.ToString("O"));
    }

    public static void Install(bool desktopShortcut, bool autostart)
    {
        if (!IsInstalled)
        {
            StopRunningInstances();
            Directory.CreateDirectory(InstallDirectory);
            File.Copy(CurrentExePath, InstalledExePath, overwrite: true);
        }

        CreateShortcut(StartMenuShortcutPath, InstalledExePath);
        if (desktopShortcut)
            CreateShortcut(DesktopShortcutPath, InstalledExePath);

        if (autostart)
            AutostartManager.Enable(InstalledExePath);

        RegisterUninstallEntry();

        try { if (File.Exists(PortableFlagPath)) File.Delete(PortableFlagPath); } catch { }
    }

    /// <summary>
    /// Removes the shortcuts, the auto-start entry and the "Apps &amp;
    /// features" registration, stops every running Agentower/remote-bot
    /// process and schedules the deletion of the program directory (and,
    /// optionally, the user data) for right after this process exits.
    /// Returns false if the user cancelled the confirmation.
    /// </summary>
    public static bool Uninstall(bool quiet)
    {
        if (!quiet)
        {
            var confirm = System.Windows.Forms.MessageBox.Show(
                "¿Desinstalar Agentower?\n\nSe quitarán el auto-inicio, los accesos directos y la aplicación instalada.",
                "Agentower", System.Windows.Forms.MessageBoxButtons.YesNo, System.Windows.Forms.MessageBoxIcon.Question);
            if (confirm != System.Windows.Forms.DialogResult.Yes)
                return false;
        }

        bool purgeData = true;
        if (!quiet)
        {
            var data = System.Windows.Forms.MessageBox.Show(
                "¿Borrar también la configuración guardada (token de Telegram, workspace, estado y logs)?\n\n" +
                "Elige «No» para conservarla por si reinstalas.",
                "Agentower", System.Windows.Forms.MessageBoxButtons.YesNo, System.Windows.Forms.MessageBoxIcon.Question);
            purgeData = data == System.Windows.Forms.DialogResult.Yes;
        }

        StopRunningInstances();
        AutostartManager.Disable();
        TryDelete(StartMenuShortcutPath);
        TryDelete(DesktopShortcutPath);
        RemoveUninstallEntry();

        var toRemove = new List<string> { InstallDirectory };
        if (purgeData)
        {
            toRemove.Add(AppPaths.LocalDirectory);
            toRemove.Add(AppPaths.SupportDirectory);
        }
        ScheduleRemoval(toRemove);
        return true;
    }

    public static void StartInstalled()
    {
        try
        {
            Process.Start(new ProcessStartInfo(InstalledExePath) { UseShellExecute = true });
        }
        catch (Exception ex)
        {
            System.Windows.Forms.MessageBox.Show(
                "No se pudo iniciar la copia instalada: " + ex.Message, "Agentower");
        }
    }

    private static void RegisterUninstallEntry()
    {
        try
        {
            using var key = Registry.CurrentUser.CreateSubKey(UninstallKey);
            key.SetValue("DisplayName", "Agentower");
            key.SetValue("DisplayVersion", Version);
            key.SetValue("Publisher", "Agentower");
            key.SetValue("DisplayIcon", InstalledExePath);
            key.SetValue("InstallLocation", InstallDirectory);
            key.SetValue("UninstallString", $"\"{InstalledExePath}\" --uninstall");
            key.SetValue("QuietUninstallString", $"\"{InstalledExePath}\" --uninstall --quiet");
            key.SetValue("NoModify", 1, RegistryValueKind.DWord);
            key.SetValue("NoRepair", 1, RegistryValueKind.DWord);
            try
            {
                long bytes = new FileInfo(InstalledExePath).Length;
                key.SetValue("EstimatedSize", (int)(bytes / 1024), RegistryValueKind.DWord);
            }
            catch
            {
                // size is cosmetic
            }
        }
        catch
        {
            // registration is best effort
        }
    }

    private static void RemoveUninstallEntry()
    {
        try { Registry.CurrentUser.DeleteSubKeyTree(UninstallKey, throwOnMissingSubKey: false); } catch { }
    }

    /// <summary>Kills every Agentower / remote-bot process except this one.</summary>
    private static void StopRunningInstances()
    {
        int self = Environment.ProcessId;
        foreach (string name in new[] { "Agentower", "remote-bot" })
        {
            foreach (Process proc in Process.GetProcessesByName(name))
            {
                if (proc.Id == self)
                    continue;
                try
                {
                    proc.Kill(entireProcessTree: true);
                    proc.WaitForExit(3000);
                }
                catch
                {
                    // already exited or inaccessible
                }
                finally
                {
                    proc.Dispose();
                }
            }
        }
    }

    /// <summary>
    /// Deletes the given directories with a detached cmd that waits a couple
    /// of seconds, so this (running) process can exit before its own exe is
    /// removed.
    /// </summary>
    private static void ScheduleRemoval(IEnumerable<string> directories)
    {
        var parts = new List<string> { "ping -n 4 127.0.0.1 >nul" };
        foreach (string dir in directories)
        {
            if (!string.IsNullOrWhiteSpace(dir))
                parts.Add($"rmdir /s /q \"{dir}\" 2>nul");
        }
        try
        {
            Process.Start(new ProcessStartInfo("cmd.exe", "/c " + string.Join(" & ", parts))
            {
                UseShellExecute = false,
                CreateNoWindow = true,
                WindowStyle = ProcessWindowStyle.Hidden,
            });
        }
        catch
        {
            // best effort
        }
    }

    private static void CreateShortcut(string shortcutPath, string targetPath)
    {
        try
        {
            string? dir = Path.GetDirectoryName(shortcutPath);
            if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);

            Type? shellType = Type.GetTypeFromProgID("WScript.Shell");
            if (shellType == null) return;

            dynamic shell = Activator.CreateInstance(shellType)!;
            dynamic shortcut = shell.CreateShortcut(shortcutPath);
            shortcut.TargetPath = targetPath;
            shortcut.WorkingDirectory = InstallDirectory;
            shortcut.Description = "Agentower";
            shortcut.IconLocation = targetPath + ",0";
            shortcut.Save();
        }
        catch
        {
            // Shortcuts are a convenience; failing to create one must not
            // abort the install.
        }
    }

    private static void TryDelete(string path)
    {
        try { if (File.Exists(path)) File.Delete(path); } catch { }
    }

    private static string Normalize(string path)
    {
        try { return Path.GetFullPath(path); } catch { return path; }
    }
}

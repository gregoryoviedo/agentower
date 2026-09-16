using System.Diagnostics;

namespace Agentower;

/// <summary>
/// Per-user "install" support. The app is a single self-contained exe, so
/// there is no MSI: on first launch it offers to copy itself into
/// %LOCALAPPDATA%\Programs\Agentower, drop Start Menu / Desktop shortcuts
/// and register itself to start with Windows. Everything lives in the
/// current user's profile, so no elevation is required.
/// </summary>
internal static class Installer
{
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
            StopRunningInstalledCopy();
            Directory.CreateDirectory(InstallDirectory);
            File.Copy(CurrentExePath, InstalledExePath, overwrite: true);
        }

        CreateShortcut(StartMenuShortcutPath, InstalledExePath);
        if (desktopShortcut)
            CreateShortcut(DesktopShortcutPath, InstalledExePath);

        if (autostart)
            AutostartManager.Enable(InstalledExePath);

        try { if (File.Exists(PortableFlagPath)) File.Delete(PortableFlagPath); } catch { }
    }

    public static void Uninstall()
    {
        AutostartManager.Disable();
        TryDelete(StartMenuShortcutPath);
        TryDelete(DesktopShortcutPath);
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

    /// <summary>
    /// Stops an already-installed copy that is running, so an update can
    /// overwrite the exe and start the new build.
    /// </summary>
    private static void StopRunningInstalledCopy()
    {
        int self = Environment.ProcessId;
        string target = Normalize(InstalledExePath);
        foreach (Process proc in Process.GetProcessesByName("Agentower"))
        {
            if (proc.Id == self) continue;
            try
            {
                string? path = proc.MainModule?.FileName;
                if (path != null && string.Equals(Normalize(path), target, StringComparison.OrdinalIgnoreCase))
                {
                    proc.Kill(entireProcessTree: true);
                    proc.WaitForExit(3000);
                }
            }
            catch
            {
                // process may have exited or be inaccessible; ignore
            }
            finally
            {
                proc.Dispose();
            }
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

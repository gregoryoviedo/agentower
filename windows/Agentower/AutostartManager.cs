using Microsoft.Win32;

namespace Agentower;

/// <summary>
/// Auto-start at login via the per-user Run key. Equivalent to the macOS
/// SMAppService login item.
/// </summary>
internal static class AutostartManager
{
    private const string RunKey = @"Software\Microsoft\Windows\CurrentVersion\Run";
    private const string ValueName = "Agentower";

    public static bool IsEnabled
    {
        get
        {
            using var key = Registry.CurrentUser.OpenSubKey(RunKey, writable: false);
            return key?.GetValue(ValueName) is string s && s.Length > 0;
        }
    }

    public static void Enable() => Enable(ExecutablePath());

    public static void Enable(string exePath)
    {
        using var key = Registry.CurrentUser.OpenSubKey(RunKey, writable: true)
            ?? Registry.CurrentUser.CreateSubKey(RunKey, writable: true);
        key.SetValue(ValueName, "\"" + exePath + "\"");
    }

    public static void Disable()
    {
        using var key = Registry.CurrentUser.OpenSubKey(RunKey, writable: true);
        key?.DeleteValue(ValueName, throwOnMissingValue: false);
    }

    private static string ExecutablePath() =>
        Environment.ProcessPath
        ?? System.Windows.Forms.Application.ExecutablePath;
}

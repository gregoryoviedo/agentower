namespace Agentower;

/// <summary>
/// Windows locations used by the wrapper. Mirrors the macOS AppPaths
/// (Application Support / Logs) using %APPDATA% and %LOCALAPPDATA%.
/// </summary>
internal static class AppPaths
{
    public static string SupportDirectory { get; } = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData), "Agentower");

    public static string LocalDirectory { get; } = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Agentower");

    public static string LogsDirectory { get; } = Path.Combine(LocalDirectory, "logs");

    public static string SettingsFile => Path.Combine(SupportDirectory, "settings.json");

    public static string EnvFile => Path.Combine(SupportDirectory, ".env");

    public static string StateDb => Path.Combine(SupportDirectory, "state.db");

    public static string ControlFile => Path.Combine(SupportDirectory, "control.json");

    public static string BotLogFile => Path.Combine(LogsDirectory, "bot.log");

    public static string WrapperLogFile => Path.Combine(LogsDirectory, "agentower.log");

    /// <summary>Where the embedded remote-bot.exe is extracted at runtime.</summary>
    public static string ExtractedBotPath => Path.Combine(LocalDirectory, "remote-bot.exe");

    public static void EnsureDirectories()
    {
        Directory.CreateDirectory(SupportDirectory);
        Directory.CreateDirectory(LogsDirectory);
    }
}

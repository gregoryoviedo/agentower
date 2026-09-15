using System.Text;
using System.Text.Json;

namespace Agentower;

/// <summary>
/// Persists the wrapper configuration as JSON in %APPDATA%\Agentower and
/// regenerates the .env the Go bot reads (mirrors the macOS ConfigStore).
/// </summary>
internal sealed class ConfigStore
{
    public static readonly string[] AgentKinds =
        { "opencode", "claude", "kiro", "copilot", "codex", "antigravity" };

    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        WriteIndented = true,
    };

    public static int DefaultPort(string kind) => kind switch
    {
        "opencode" => 4096,
        "claude" => 4097,
        "kiro" => 4099,
        "copilot" => 4100,
        "codex" => 4101,
        "antigravity" => 4102,
        _ => 4096,
    };

    public static string DisplayName(string kind) => kind switch
    {
        "opencode" => "opencode",
        "claude" => "Claude",
        "kiro" => "Kiro",
        "copilot" => "GitHub Copilot",
        "codex" => "Codex",
        "antigravity" => "Antigravity",
        _ => kind,
    };

    public BotConfiguration Load()
    {
        BotConfiguration cfg;
        try
        {
            if (File.Exists(AppPaths.SettingsFile))
            {
                string json = File.ReadAllText(AppPaths.SettingsFile);
                cfg = JsonSerializer.Deserialize<BotConfiguration>(json, JsonOptions) ?? new BotConfiguration();
            }
            else
            {
                cfg = new BotConfiguration();
            }
        }
        catch
        {
            cfg = new BotConfiguration();
        }

        cfg.Agents ??= new Dictionary<string, AgentSettings>();
        var detected = AgentDetector.Detect();
        foreach (string kind in AgentKinds)
        {
            cfg.Agents.TryGetValue(kind, out AgentSettings? stored);
            bool available = kind == "opencode" || detected.ContainsKey(kind);
            string resolvedBin = !string.IsNullOrEmpty(stored?.Bin)
                ? stored!.Bin
                : detected.TryGetValue(kind, out string? p) ? p : kind;
            int resolvedPort = stored is { Port: > 0 } ? stored.Port : DefaultPort(kind);
            bool resolvedEnabled = stored?.Enabled ?? available;
            cfg.Agents[kind] = new AgentSettings
            {
                Enabled = resolvedEnabled,
                Bin = resolvedBin,
                Port = resolvedPort,
                Args = stored?.Args ?? "",
                Available = available,
                DetectedPath = detected.TryGetValue(kind, out string? dp) ? dp : null,
            };
        }
        return cfg;
    }

    public void Save(BotConfiguration config)
    {
        AppPaths.EnsureDirectories();
        string json = JsonSerializer.Serialize(config, JsonOptions);
        File.WriteAllText(AppPaths.SettingsFile, json, new UTF8Encoding(false));
        WriteEnvFile(config);
    }

    private static void WriteEnvFile(BotConfiguration config)
    {
        var lines = new List<string>
        {
            $"WORKSPACE_ROOT={ShellQuote(config.WorkspaceRoot)}",
            $"TELEGRAM_BOT_TOKEN={ShellQuote(config.TelegramBotToken)}",
            $"ALLOWED_CHAT_ID={ShellQuote(config.AllowedChatID.Trim())}",
        };

        foreach (var (kind, settings) in config.Agents)
        {
            string prefix = "AGENT_" + kind.ToUpperInvariant();
            lines.Add($"{prefix}_ENABLED={(settings.Enabled ? "true" : "false")}");
            if (!string.IsNullOrEmpty(settings.Bin))
                lines.Add($"{prefix}_BIN={ShellQuote(settings.Bin)}");
            if (settings.Port > 0)
                lines.Add($"{prefix}_PORT={settings.Port}");
            string trimmedArgs = settings.Args.Trim();
            if (!string.IsNullOrEmpty(trimmedArgs))
                lines.Add($"{prefix}_ARGS={ShellQuote(trimmedArgs)}");
        }

        if (!string.IsNullOrEmpty(config.AgentowerStatePath))
            lines.Add($"AGENTOWER_STATE_PATH={ShellQuote(config.AgentowerStatePath)}");
        if (!string.IsNullOrEmpty(config.TelegramAPIRoot))
            lines.Add($"TELEGRAM_API_ROOT={ShellQuote(config.TelegramAPIRoot)}");
        if (!string.IsNullOrEmpty(config.TelegramProxyURL))
            lines.Add($"TELEGRAM_PROXY_URL={ShellQuote(config.TelegramProxyURL)}");

        File.WriteAllText(AppPaths.EnvFile, string.Join('\n', lines) + "\n", new UTF8Encoding(false));
    }

    /// <summary>
    /// Quotes an .env value the way the macOS wrapper does. Backslashes
    /// (Windows paths) are escaped because godotenv unescapes
    /// double-quoted values.
    /// </summary>
    private static string ShellQuote(string value)
    {
        bool simple = value.Length > 0 && value.All(c =>
            char.IsLetterOrDigit(c) || "/._-:@?=+&%".Contains(c));
        if (simple)
            return value;
        string escaped = value
            .Replace("\\", "\\\\")
            .Replace("\"", "\\\"")
            .Replace("\r", "")
            .Replace("\n", "\\n");
        return "\"" + escaped + "\"";
    }
}

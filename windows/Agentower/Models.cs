using System.Text.Json.Serialization;

namespace Agentower;

/// <summary>Per-agent settings row (mirrors the macOS AgentSettings).</summary>
internal sealed class AgentSettings
{
    public bool Enabled { get; set; }
    public string Bin { get; set; } = "";
    public int Port { get; set; }
    public string Args { get; set; } = "";

    [JsonIgnore]
    public bool Available { get; set; }

    [JsonIgnore]
    public string? DetectedPath { get; set; }
}

/// <summary>Root configuration persisted to %APPDATA%\Agentower\settings.json.</summary>
internal sealed class BotConfiguration
{
    public string WorkspaceRoot { get; set; } = "";
    public string TelegramBotToken { get; set; } = "";
    public string AllowedChatID { get; set; } = "";
    public string AgentowerStatePath { get; set; } = "";
    public string TelegramAPIRoot { get; set; } = "";
    public string TelegramProxyURL { get; set; } = "";
    public Dictionary<string, AgentSettings> Agents { get; set; } = new();

    [JsonIgnore]
    public bool IsValid =>
        !string.IsNullOrWhiteSpace(WorkspaceRoot)
        && !string.IsNullOrWhiteSpace(TelegramBotToken)
        && long.TryParse(AllowedChatID.Trim(), out long id) && id != 0;

    [JsonIgnore]
    public string? ValidationMessage
    {
        get
        {
            if (string.IsNullOrWhiteSpace(WorkspaceRoot)) return "WORKSPACE_ROOT es obligatorio.";
            if (string.IsNullOrWhiteSpace(TelegramBotToken)) return "TELEGRAM_BOT_TOKEN es obligatorio.";
            if (!long.TryParse(AllowedChatID.Trim(), out long id) || id == 0) return "ALLOWED_CHAT_ID debe ser un entero distinto de cero.";
            return null;
        }
    }
}

internal enum BotState
{
    Stopped,
    Starting,
    Running,
    Stopping,
    Crashed,
}

internal sealed class BotStatus
{
    public BotState State { get; init; } = BotState.Stopped;
    public int Pid { get; init; }
    public int ExitCode { get; init; }

    public bool IsRunning => State is BotState.Running or BotState.Starting;

    public string Label => State switch
    {
        BotState.Stopped => "Detenido",
        BotState.Starting => "Iniciando…",
        BotState.Running => $"En ejecución (PID {Pid})",
        BotState.Stopping => "Deteniendo…",
        BotState.Crashed => $"Salida con código {ExitCode}",
        _ => "Desconocido",
    };
}

// ---- Local control server DTOs (Go field names, no JSON tags) ----

internal sealed class Snapshot
{
    public long ChatID { get; set; }
    public string? ActiveProject { get; set; }
    public string? ActiveSession { get; set; }
    public string? ActiveAgent { get; set; }
    public long PendingNotifChat { get; set; }
    public CompletedSession? LastCompleted { get; set; }
    public PendingQuestion? PendingQuestion { get; set; }
}

internal sealed class PendingQuestion
{
    public long ChatID { get; set; }
    public string SessionID { get; set; } = "";
    public string RequestID { get; set; } = "";
}

internal sealed class CompletedSession
{
    public long ChatID { get; set; }
    public string SessionID { get; set; } = "";
    public string? ProjectName { get; set; }
    public string? Directory { get; set; }
    public string? Title { get; set; }
    public string? Preview { get; set; }
    public string? AgentKind { get; set; }
    public DateTimeOffset CompletedAt { get; set; }
    public DateTimeOffset NotifiedAt { get; set; }
}

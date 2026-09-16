using System.Net.Http;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;
using System.Windows.Forms;

namespace Agentower;

/// <summary>
/// Windows equivalent of the macOS IdleNotifier. Watches how long the
/// user has been away (GetLastInputInfo), polls the bot's local control
/// server for a just-completed session, and — when both hold — asks the
/// bot to push the "Tarea completada" Telegram message and shows a
/// tray notification.
/// </summary>
internal sealed class IdleNotifier : IDisposable
{
    private static readonly TimeSpan IdleThreshold = TimeSpan.FromMinutes(2);
    private static readonly TimeSpan QuestionIdleThreshold = TimeSpan.FromMinutes(1);
    private static readonly TimeSpan CompletionFreshness = TimeSpan.FromHours(1);
    private static readonly TimeSpan CooldownAfterNotify = TimeSpan.FromSeconds(60);

    private readonly HttpClient _http;
    private readonly System.Windows.Forms.Timer _timer;
    private readonly Action<string, string> _onNotify;
    private DateTimeOffset? _lastNotifySentAt;
    private string? _lastQuestionRequestId;

    public IdleNotifier(Action<string, string> onNotify)
    {
        _onNotify = onNotify;
        _http = new HttpClient { Timeout = TimeSpan.FromSeconds(4) };
        _timer = new System.Windows.Forms.Timer { Interval = 5000 };
        _timer.Tick += async (_, _) => await TickAsync();
    }

    public void Start() => _timer.Start();

    public void Stop() => _timer.Stop();

    private async Task TickAsync()
    {
        try
        {
            string? addr = ReadControlAddress();
            if (addr == null) return;

            Snapshot? state = await FetchStateAsync(addr);
            if (state == null) return;

            // A pending question wins over completion: while the agent is
            // blocked waiting for an answer, ping the user with the
            // question rather than a "task done" message.
            if (state.PendingQuestion is { } question)
            {
                if (IdleTime() < QuestionIdleThreshold) return;
                if (_lastQuestionRequestId == question.RequestID) return;
                (bool questionSent, bool stopRetrying) = await NotifyQuestionAsync(addr, question.ChatID, question.RequestID);
                if (questionSent)
                {
                    _lastQuestionRequestId = question.RequestID;
                    _onNotify(
                        "El agente necesita tu respuesta",
                        "El agente hizo una pregunta y está esperando. Responde desde Telegram.");
                }
                else if (stopRetrying)
                {
                    // 409: already notified or already answered.
                    _lastQuestionRequestId = question.RequestID;
                }
                return;
            }

            if (IdleTime() < IdleThreshold) return;

            CompletedSession? last = state.LastCompleted;
            if (last == null) return;

            TimeSpan age = DateTimeOffset.UtcNow - last.CompletedAt;
            if (age < TimeSpan.Zero || age > CompletionFreshness) return;

            if (state.PendingNotifChat == 0) return;

            if (_lastNotifySentAt is { } sent && DateTimeOffset.UtcNow - sent < CooldownAfterNotify) return;

            bool ok = await NotifyAsync(addr, state.PendingNotifChat);
            if (ok)
            {
                _lastNotifySentAt = DateTimeOffset.UtcNow;
                string project = FirstNonEmpty(last.ProjectName, last.Directory, "tu proyecto");
                _onNotify(
                    "Tarea de Agentower terminada",
                    $"{project}: Agentower terminó mientras no estabas. Revisa Telegram o vuelve a la laptop.");
            }
        }
        catch
        {
            // best effort; retry on the next tick
        }
    }

    private string? ReadControlAddress()
    {
        try
        {
            if (!File.Exists(AppPaths.ControlFile)) return null;
            string json = File.ReadAllText(AppPaths.ControlFile);
            using var doc = JsonDocument.Parse(json);
            if (doc.RootElement.TryGetProperty("addr", out var addr) && addr.ValueKind == JsonValueKind.String)
            {
                string? value = addr.GetString();
                return string.IsNullOrWhiteSpace(value) ? null : value;
            }
        }
        catch
        {
            // file may be mid-write; retry next tick
        }
        return null;
    }

    private async Task<Snapshot?> FetchStateAsync(string addr)
    {
        try
        {
            using var resp = await _http.GetAsync($"http://{addr}/state");
            if (!resp.IsSuccessStatusCode) return null;
            string body = await resp.Content.ReadAsStringAsync();
            return JsonSerializer.Deserialize<Snapshot>(body, new JsonSerializerOptions { PropertyNameCaseInsensitive = true });
        }
        catch
        {
            return null;
        }
    }

    private async Task<bool> NotifyAsync(string addr, long chatId)
    {
        try
        {
            string body = JsonSerializer.Serialize(new { chat_id = chatId });
            using var content = new StringContent(body, Encoding.UTF8, "application/json");
            using var resp = await _http.PostAsync($"http://{addr}/notify", content);
            return resp.IsSuccessStatusCode;
        }
        catch
        {
            return false;
        }
    }

    private async Task<(bool Sent, bool StopRetrying)> NotifyQuestionAsync(string addr, long chatId, string requestId)
    {
        try
        {
            string body = JsonSerializer.Serialize(new { chat_id = chatId, request_id = requestId });
            using var content = new StringContent(body, Encoding.UTF8, "application/json");
            using var resp = await _http.PostAsync($"http://{addr}/question-notify", content);
            if (resp.IsSuccessStatusCode) return (true, true);
            if ((int)resp.StatusCode == 409) return (false, true);
            return (false, false);
        }
        catch
        {
            return (false, false);
        }
    }

    private static string FirstNonEmpty(params string?[] values)
    {
        foreach (string? v in values)
            if (!string.IsNullOrWhiteSpace(v))
                return v!;
        return "";
    }

    private static TimeSpan IdleTime()
    {
        var info = new LASTINPUTINFO { cbSize = (uint)Marshal.SizeOf<LASTINPUTINFO>() };
        if (!GetLastInputInfo(ref info)) return TimeSpan.Zero;
        uint idle = unchecked((uint)Environment.TickCount - info.dwTime);
        return TimeSpan.FromMilliseconds(idle);
    }

    public void Dispose()
    {
        _timer.Dispose();
        _http.Dispose();
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct LASTINPUTINFO
    {
        public uint cbSize;
        public uint dwTime;
    }

    [DllImport("user32.dll")]
    private static extern bool GetLastInputInfo(ref LASTINPUTINFO plii);
}

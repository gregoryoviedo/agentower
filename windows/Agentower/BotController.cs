using System.Diagnostics;
using System.Text;

namespace Agentower;

/// <summary>
/// Owns the embedded remote-bot.exe subprocess: extracts it, spawns it
/// with the .env/state env vars, streams its output to the log file and
/// stops the whole process tree on shutdown (mirrors the macOS
/// BotController).
/// </summary>
internal sealed class BotController : IDisposable
{
    private const string BinaryResourceName = "Agentower.Resources.remote-bot.exe";

    private readonly object _gate = new();
    private SynchronizationContext? _sync;
    private Process? _process;
    private StreamWriter? _log;

    public BotStatus Status { get; private set; } = new();
    public string? LastError { get; private set; }
    public bool IsRunning => Status.IsRunning;

    public event Action<BotStatus>? StatusChanged;

    public void Start(string telegramApiRoot, string telegramProxyUrl)
    {
        lock (_gate)
        {
            if (IsRunning) return;
            // Start is invoked from a UI event, so the WinForms
            // SynchronizationContext is available here even though it did
            // not exist when the controller was constructed.
            _sync ??= SynchronizationContext.Current;
            try
            {
                AppPaths.EnsureDirectories();
                EnsureBinaryExtracted();
                OpenLog();

                if (!File.Exists(AppPaths.EnvFile))
                    throw new InvalidOperationException("Falta el archivo .env. Configura el bot primero.");

                var psi = new ProcessStartInfo
                {
                    FileName = AppPaths.ExtractedBotPath,
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    RedirectStandardOutput = true,
                    RedirectStandardError = true,
                    WorkingDirectory = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile),
                };
                psi.Environment["ENV_FILE"] = AppPaths.EnvFile;
                psi.Environment["AGENTOWER_STATE_PATH"] = AppPaths.StateDb;
                psi.Environment["GIN_MODE"] = "release";
                if (!string.IsNullOrEmpty(telegramApiRoot))
                    psi.Environment["TELEGRAM_API_ROOT"] = telegramApiRoot;
                if (!string.IsNullOrEmpty(telegramProxyUrl))
                    psi.Environment["TELEGRAM_PROXY_URL"] = telegramProxyUrl;

                Log($"Arrancando {psi.FileName} con ENV_FILE={AppPaths.EnvFile}");

                var proc = new Process { StartInfo = psi, EnableRaisingEvents = true };
                proc.OutputDataReceived += (_, e) => WriteLine(e.Data);
                proc.ErrorDataReceived += (_, e) => WriteLine(e.Data);
                proc.Exited += (_, _) => OnExited(proc);

                if (!proc.Start())
                    throw new InvalidOperationException("No se pudo iniciar el proceso.");
                proc.BeginOutputReadLine();
                proc.BeginErrorReadLine();
                _process = proc;
                SetStatus(new BotStatus { State = BotState.Running, Pid = proc.Id });
            }
            catch (Exception ex)
            {
                LastError = ex.Message;
                Log("ERROR: " + ex.Message);
                CloseLog();
                SetStatus(new BotStatus { State = BotState.Crashed, ExitCode = -1 });
            }
        }
    }

    public void Stop()
    {
        lock (_gate)
        {
            if (_process == null || _process.HasExited)
            {
                _process = null;
                SetStatus(new BotStatus { State = BotState.Stopped });
                return;
            }
            SetStatus(new BotStatus { State = BotState.Stopping });
            try
            {
                _process.Kill(entireProcessTree: true);
                _process.WaitForExit(5000);
            }
            catch (Exception ex)
            {
                Log("ERROR deteniendo: " + ex.Message);
            }
        }
    }

    public void Shutdown() => Stop();

    private void OnExited(Process proc)
    {
        lock (_gate)
        {
            int code = -1;
            try { code = proc.ExitCode; } catch { }
            bool stopping = Status.State == BotState.Stopping;
            _process = null;
            CloseLog();
            Post(stopping
                ? new BotStatus { State = BotState.Stopped }
                : code == 0
                    ? new BotStatus { State = BotState.Stopped }
                    : new BotStatus { State = BotState.Crashed, ExitCode = code });
        }
    }

    private void EnsureBinaryExtracted()
    {
        var asm = typeof(BotController).Assembly;
        using var stream = asm.GetManifestResourceStream(BinaryResourceName)
            ?? throw new FileNotFoundException(
                "El binario remote-bot.exe no está embebido. Compila con windows/build.ps1.");
        Directory.CreateDirectory(AppPaths.LocalDirectory);
        string target = AppPaths.ExtractedBotPath;
        if (File.Exists(target) && new FileInfo(target).Length == stream.Length)
            return;
        using var fs = new FileStream(target, FileMode.Create, FileAccess.Write, FileShare.None);
        stream.CopyTo(fs);
        Log($"Binario extraído en {target}");
    }

    private void OpenLog()
    {
        Directory.CreateDirectory(AppPaths.LogsDirectory);
        _log = new StreamWriter(new FileStream(
            AppPaths.BotLogFile, FileMode.Append, FileAccess.Write, FileShare.ReadWrite), new UTF8Encoding(false))
        {
            AutoFlush = true,
        };
        _log.WriteLine($"\n--- remote-bot start at {DateTimeOffset.Now:O} ---");
    }

    private void CloseLog()
    {
        try { _log?.Dispose(); } catch { }
        _log = null;
    }

    private void WriteLine(string? line)
    {
        if (line == null) return;
        try { _log?.WriteLine(line); } catch { }
    }

    private void Log(string message)
    {
        try
        {
            Directory.CreateDirectory(AppPaths.LogsDirectory);
            File.AppendAllText(AppPaths.WrapperLogFile,
                $"[{DateTimeOffset.Now:O}] {message}{Environment.NewLine}", new UTF8Encoding(false));
        }
        catch { }
        WriteLine($"[{DateTimeOffset.Now:O}] {message}");
    }

    private void SetStatus(BotStatus status)
    {
        Status = status;
        if (status.State is not BotState.Running and not BotState.Starting)
            if (status.State == BotState.Stopped)
                LastError = null;
        Post(status);
    }

    private void Post(BotStatus status)
    {
        if (_sync != null)
            _sync.Post(_ => StatusChanged?.Invoke(status), null);
        else
            StatusChanged?.Invoke(status);
    }

    public void Dispose()
    {
        Stop();
        CloseLog();
    }
}

using System.Diagnostics;
using System.Windows.Forms;

namespace Agentower;

/// <summary>
/// Owns the notification-area icon, the context menu and the popover,
/// and wires the bot controller and the idle notifier together. Mirrors
/// the macOS StatusBarController + AppDelegate.
/// </summary>
internal sealed class TrayApplicationContext : ApplicationContext
{
    private readonly ConfigStore _store = new();
    private readonly BotController _bot = new();
    private readonly NotifyIcon _tray;
    private readonly IdleNotifier _idle;
    private readonly ContextMenuStrip _menu;
    private readonly ToolStripMenuItem _statusItem;
    private readonly ToolStripMenuItem _toggleItem;

    private BotConfiguration _config;
    private DateTimeOffset? _startedAt;
    private PopoverForm? _popover;
    private SettingsForm? _settings;

    public TrayApplicationContext()
    {
        _config = _store.Load();

        _menu = new ContextMenuStrip();
        _statusItem = new ToolStripMenuItem { Enabled = false };
        _toggleItem = new ToolStripMenuItem();
        _toggleItem.Click += (_, _) => Toggle();
        var settingsItem = new ToolStripMenuItem("Configuración…");
        settingsItem.Click += (_, _) => ShowSettings();
        var logItem = new ToolStripMenuItem("Abrir registro");
        logItem.Click += (_, _) => OpenLog();
        var installItem = new ToolStripMenuItem("Instalar en el sistema…");
        installItem.Click += (_, _) => InstallToSystem();
        installItem.Visible = !Installer.IsInstalled;
        var quitItem = new ToolStripMenuItem("Salir de Agentower");
        quitItem.Click += (_, _) => Quit();

        _menu.Items.Add(_statusItem);
        _menu.Items.Add(new ToolStripSeparator());
        _menu.Items.Add(_toggleItem);
        _menu.Items.Add(settingsItem);
        _menu.Items.Add(logItem);
        _menu.Items.Add(installItem);
        _menu.Items.Add(new ToolStripSeparator());
        _menu.Items.Add(quitItem);

        _tray = new NotifyIcon
        {
            Icon = IconFactory.LoadAppIcon(),
            Visible = true,
            ContextMenuStrip = _menu,
        };
        _tray.MouseClick += OnTrayMouseClick;

        _bot.StatusChanged += OnBotStatusChanged;
        _idle = new IdleNotifier(ShowBalloon);
        _idle.Start();

        UpdateUi();

        // Auto-start the bot as soon as the app is up (the app itself is
        // launched at login via the Run key). Deferred one tick so the
        // WinForms SynchronizationContext exists when the bot spawns.
        var autoStart = new System.Windows.Forms.Timer { Interval = 800 };
        autoStart.Tick += (_, _) =>
        {
            autoStart.Stop();
            autoStart.Dispose();
            AutoStartBotIfConfigured();
        };
        autoStart.Start();
    }

    private void AutoStartBotIfConfigured()
    {
        if (!_config.IsValid || _bot.IsRunning) return;
        _bot.Start(_config.TelegramAPIRoot, _config.TelegramProxyURL);
    }

    private void OnTrayMouseClick(object? sender, MouseEventArgs e)
    {
        if (e.Button != MouseButtons.Left) return;
        if (_popover is { Visible: true })
            _popover.Close();
        else
            ShowPopover();
    }

    private void ShowPopover()
    {
        _popover = new PopoverForm(
            () => _bot.Status,
            () => _config,
            () => _startedAt,
            Toggle,
            ShowSettings,
            OpenLog,
            Quit);
        _popover.FormClosed += (_, _) => _popover = null;
        _popover.ShowNearCursor();
    }

    private void Toggle()
    {
        if (_bot.IsRunning)
            _bot.Stop();
        else if (_config.IsValid)
            _bot.Start(_config.TelegramAPIRoot, _config.TelegramProxyURL);
    }

    private void OnBotStatusChanged(BotStatus status)
    {
        _startedAt = status.State == BotState.Running ? DateTimeOffset.UtcNow : null;
        UpdateUi();
        _popover?.Refresh1();
    }

    private void UpdateUi()
    {
        BotStatus status = _bot.Status;
        _statusItem.Text = status.Label;
        _toggleItem.Text = status.IsRunning ? "Detener" : "Iniciar";
        _toggleItem.Enabled = status.IsRunning || _config.IsValid;

        string tooltip = _config.IsValid
            ? $"Agentower — {status.Label}"
            : "Agentower — configura el bot";
        _tray.Text = tooltip.Length > 63 ? tooltip[..63] : tooltip;
    }

    private void ShowSettings()
    {
        if (_settings is { Visible: true })
        {
            _settings.Activate();
            return;
        }
        _settings = new SettingsForm(_store, _config);
        _settings.FormClosed += (_, _) =>
        {
            if (_settings!.Saved)
            {
                _config = _store.Load();
                UpdateUi();
                _popover?.Refresh1();
            }
            _settings = null;
        };
        _settings.Show();
        _settings.Activate();
    }

    private void OpenLog()
    {
        try
        {
            AppPaths.EnsureDirectories();
            if (!File.Exists(AppPaths.BotLogFile))
                File.WriteAllText(AppPaths.BotLogFile, "");
            Process.Start(new ProcessStartInfo(AppPaths.BotLogFile) { UseShellExecute = true });
        }
        catch (Exception ex)
        {
            MessageBox.Show("No se pudo abrir el registro: " + ex.Message, "Agentower");
        }
    }

    private void ShowBalloon(string title, string body)
    {
        _tray.BalloonTipTitle = title;
        _tray.BalloonTipText = body;
        _tray.BalloonTipIcon = ToolTipIcon.Info;
        _tray.ShowBalloonTip(5000);
    }

    private void InstallToSystem()
    {
        using var form = new InstallForm(Installer.InstalledCopyExists);
        if (form.ShowDialog() != DialogResult.OK) return;
        Installer.Install(form.DesktopShortcut, form.Autostart);
        // Hand over to the installed copy; this instance releases the
        // single-instance mutex on exit and the new one picks it up.
        Installer.StartInstalled();
        Quit();
    }

    private void Quit()
    {
        _bot.Shutdown();
        _idle.Stop();
        _tray.Visible = false;
        ExitThread();
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            _idle.Dispose();
            _bot.Dispose();
            _tray.Dispose();
            _menu.Dispose();
        }
        base.Dispose(disposing);
    }
}

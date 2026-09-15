using System.Windows.Forms;

namespace Agentower;

/// <summary>
/// The Bluetooth-style popover shown on a left click of the tray icon.
/// Mirrors the macOS PopoverContentView: status, uptime, toggle and the
/// quick actions.
/// </summary>
internal sealed class PopoverForm : Form
{
    private readonly Func<BotStatus> _getStatus;
    private readonly Func<BotConfiguration> _getConfig;
    private readonly Func<DateTimeOffset?> _getStartedAt;
    private readonly Action _onToggle;

    private readonly CheckBox _toggle;
    private readonly Label _statusLabel;
    private readonly Label _uptimeLabel;
    private readonly Label _errorLabel;
    private readonly System.Windows.Forms.Timer _refresh;

    public PopoverForm(
        Func<BotStatus> getStatus,
        Func<BotConfiguration> getConfig,
        Func<DateTimeOffset?> getStartedAt,
        Action onToggle,
        Action onSettings,
        Action onOpenLog,
        Action onQuit)
    {
        _getStatus = getStatus;
        _getConfig = getConfig;
        _getStartedAt = getStartedAt;
        _onToggle = onToggle;

        FormBorderStyle = FormBorderStyle.FixedSingle;
        StartPosition = FormStartPosition.Manual;
        ShowInTaskbar = false;
        TopMost = true;
        MinimizeBox = false;
        MaximizeBox = false;
        Text = "Agentower";
        ClientSize = new Size(320, 220);
        Font = new Font("Segoe UI", 9F);
        BackColor = Color.White;

        var layout = new TableLayoutPanel
        {
            Dock = DockStyle.Fill,
            ColumnCount = 1,
            Padding = new Padding(14),
            AutoSize = true,
        };

        var header = new Panel { Dock = DockStyle.Top, Height = 30 };
        var title = new Label
        {
            Text = "Agentower",
            Font = new Font("Segoe UI", 11F, FontStyle.Bold),
            AutoSize = true,
            Location = new Point(0, 4),
        };
        _toggle = new CheckBox
        {
            Appearance = Appearance.Button,
            Text = "Activo",
            TextAlign = ContentAlignment.MiddleCenter,
            Size = new Size(80, 26),
            Anchor = AnchorStyles.Top | AnchorStyles.Right,
        };
        _toggle.CheckedChanged += (_, _) => _onToggle();
        header.Controls.Add(title);
        header.Controls.Add(_toggle);
        header.Resize += (_, _) => _toggle.Location = new Point(header.Width - _toggle.Width, 0);

        _statusLabel = new Label { AutoSize = true, ForeColor = Color.DimGray };
        _uptimeLabel = new Label { AutoSize = true, ForeColor = Color.Gray };
        _errorLabel = new Label { AutoSize = true, ForeColor = Color.Firebrick, MaximumSize = new Size(290, 0) };

        var separator = new Label { BorderStyle = BorderStyle.Fixed3D, Height = 2, Width = 290 };

        var actions = new FlowLayoutPanel
        {
            FlowDirection = FlowDirection.TopDown,
            AutoSize = true,
            WrapContents = false,
        };
        actions.Controls.Add(ActionButton("Configuración…", onSettings));
        actions.Controls.Add(ActionButton("Abrir registro", onOpenLog));
        actions.Controls.Add(ActionButton("Salir de Agentower", onQuit));

        layout.Controls.Add(header);
        layout.Controls.Add(_statusLabel);
        layout.Controls.Add(_uptimeLabel);
        layout.Controls.Add(_errorLabel);
        layout.Controls.Add(separator);
        layout.Controls.Add(actions);
        Controls.Add(layout);

        _refresh = new System.Windows.Forms.Timer { Interval = 1000 };
        _refresh.Tick += (_, _) => Refresh1();
        Shown += (_, _) => { Refresh1(); _refresh.Start(); };
        FormClosed += (_, _) => _refresh.Stop();
        Deactivate += (_, _) => { if (Visible) Close(); };
    }

    private static Button ActionButton(string text, Action action)
    {
        var button = new Button
        {
            Text = text,
            TextAlign = ContentAlignment.MiddleLeft,
            FlatStyle = FlatStyle.Flat,
            Width = 288,
            Height = 30,
            Margin = new Padding(0, 2, 0, 2),
        };
        button.FlatAppearance.BorderSize = 0;
        button.Click += (_, _) => action();
        return button;
    }

    public void ShowNearCursor()
    {
        var screen = Screen.FromPoint(Cursor.Position).WorkingArea;
        int x = Math.Min(Cursor.Position.X, screen.Right - Width);
        int y = Math.Max(screen.Top, Cursor.Position.Y - Height);
        Location = new Point(x, y);
        Show();
        Activate();
    }

    public void Refresh1()
    {
        BotStatus status = _getStatus();
        BotConfiguration config = _getConfig();

        if (_toggle.Checked != status.IsRunning)
            _toggle.Checked = status.IsRunning;
        _toggle.Enabled = config.IsValid;

        _statusLabel.Text = status.Label;

        DateTimeOffset? started = _getStartedAt();
        if (status.State == BotState.Running && started is { } s)
        {
            TimeSpan up = DateTimeOffset.UtcNow - s;
            _uptimeLabel.Text = "Tiempo activo " + Format(up);
            _uptimeLabel.Visible = true;
        }
        else
        {
            _uptimeLabel.Visible = false;
        }

        string? error = config.IsValid ? null : config.ValidationMessage;
        _errorLabel.Text = error ?? "";
        _errorLabel.Visible = !string.IsNullOrEmpty(error);
    }

    private static string Format(TimeSpan span)
    {
        if (span.TotalHours >= 1)
            return $"{(int)span.TotalHours} h {span.Minutes} min";
        if (span.TotalMinutes >= 1)
            return $"{(int)span.TotalMinutes} min {span.Seconds} s";
        return $"{span.Seconds} s";
    }
}

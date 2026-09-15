using System.Windows.Forms;

namespace Agentower;

/// <summary>
/// Settings window with the same four sections as the macOS app:
/// Telegram, Agentes de IA, Avanzado and Inicio.
/// </summary>
internal sealed class SettingsForm : Form
{
    private readonly ConfigStore _store;
    private readonly BotConfiguration _config;

    public bool Saved { get; private set; }

    private TextBox _workspace = null!;
    private TextBox _token = null!;
    private TextBox _chatId = null!;
    private TextBox _statePath = null!;
    private TextBox _apiRoot = null!;
    private TextBox _proxyUrl = null!;
    private CheckBox _autostart = null!;
    private FlowLayoutPanel _agentsPanel = null!;
    private Label _message = null!;
    private readonly List<AgentRow> _rows = new();

    public SettingsForm(ConfigStore store, BotConfiguration config)
    {
        _store = store;
        _config = config;

        Text = "Agentower — Configuración";
        Font = new Font("Segoe UI", 9F);
        ClientSize = new Size(600, 560);
        StartPosition = FormStartPosition.CenterScreen;
        MinimizeBox = false;
        MaximizeBox = false;
        FormBorderStyle = FormBorderStyle.FixedDialog;
        ShowInTaskbar = true;

        var tabs = new TabControl { Dock = DockStyle.Fill };
        tabs.TabPages.Add(BuildTelegramTab());
        tabs.TabPages.Add(BuildAgentsTab());
        tabs.TabPages.Add(BuildAdvancedTab());
        tabs.TabPages.Add(BuildStartupTab());

        var bottom = new Panel { Dock = DockStyle.Bottom, Height = 52, Padding = new Padding(12, 10, 12, 10) };
        _message = new Label { AutoSize = true, ForeColor = Color.Firebrick, Location = new Point(12, 18) };
        var save = new Button
        {
            Text = "Guardar",
            DialogResult = DialogResult.None,
            Size = new Size(90, 28),
            Location = new Point(bottom.Width - 200, 12),
            Anchor = AnchorStyles.Top | AnchorStyles.Right,
        };
        var cancel = new Button
        {
            Text = "Cancelar",
            Size = new Size(90, 28),
            Location = new Point(bottom.Width - 102, 12),
            Anchor = AnchorStyles.Top | AnchorStyles.Right,
        };
        save.Click += (_, _) => Save();
        cancel.Click += (_, _) => Close();
        bottom.Controls.Add(_message);
        bottom.Controls.Add(save);
        bottom.Controls.Add(cancel);
        bottom.Resize += (_, _) =>
        {
            cancel.Location = new Point(bottom.Width - 102, 12);
            save.Location = new Point(bottom.Width - 200, 12);
        };

        Controls.Add(tabs);
        Controls.Add(bottom);

        LoadValues();
    }

    // ---- Tabs ----

    private TabPage BuildTelegramTab()
    {
        var page = new TabPage("Telegram") { Padding = new Padding(16) };
        var table = NewTable();
        _workspace = new TextBox { Dock = DockStyle.Fill };
        var pick = new Button { Text = "Elegir…", Width = 90, Dock = DockStyle.Right };
        pick.Click += (_, _) => PickWorkspace();
        var workspaceRow = new Panel { Dock = DockStyle.Fill, Height = 26 };
        workspaceRow.Controls.Add(_workspace);
        workspaceRow.Controls.Add(pick);
        AddRow(table, "Carpeta del workspace", workspaceRow);
        _token = new TextBox { Dock = DockStyle.Fill, UseSystemPasswordChar = true };
        AddRow(table, "Token del bot", _token);
        _chatId = new TextBox { Dock = DockStyle.Fill };
        AddRow(table, "ID del chat permitido", _chatId);
        page.Controls.Add(table);
        return page;
    }

    private TabPage BuildAgentsTab()
    {
        var page = new TabPage("Agentes") { Padding = new Padding(12) };
        var header = new Panel { Dock = DockStyle.Top, Height = 40 };
        var hint = new Label
        {
            Text = "Detecta los binarios en PATH (y las extensiones de VS Code para Copilot).",
            AutoSize = true,
            ForeColor = Color.DimGray,
            Location = new Point(0, 10),
        };
        var retry = new Button
        {
            Text = "Reintentar detección",
            Size = new Size(150, 26),
            Anchor = AnchorStyles.Top | AnchorStyles.Right,
        };
        retry.Click += (_, _) => RunDetection();
        header.Controls.Add(hint);
        header.Controls.Add(retry);
        header.Resize += (_, _) => retry.Location = new Point(header.Width - retry.Width, 6);

        _agentsPanel = new FlowLayoutPanel
        {
            Dock = DockStyle.Fill,
            AutoScroll = true,
            FlowDirection = FlowDirection.TopDown,
            WrapContents = false,
        };

        foreach (string kind in ConfigStore.AgentKinds)
        {
            var row = new AgentRow(kind);
            _rows.Add(row);
            _agentsPanel.Controls.Add(row.Container);
        }

        page.Controls.Add(_agentsPanel);
        page.Controls.Add(header);
        return page;
    }

    private TabPage BuildAdvancedTab()
    {
        var page = new TabPage("Avanzado") { Padding = new Padding(16) };
        var table = NewTable();
        _statePath = new TextBox { Dock = DockStyle.Fill };
        AddRow(table, "AGENTOWER_STATE_PATH", _statePath);
        _apiRoot = new TextBox { Dock = DockStyle.Fill };
        AddRow(table, "TELEGRAM_API_ROOT", _apiRoot);
        _proxyUrl = new TextBox { Dock = DockStyle.Fill };
        AddRow(table, "TELEGRAM_PROXY_URL", _proxyUrl);
        page.Controls.Add(table);
        return page;
    }

    private TabPage BuildStartupTab()
    {
        var page = new TabPage("Inicio") { Padding = new Padding(16) };
        _autostart = new CheckBox
        {
            Text = "Iniciar Agentower al arrancar Windows",
            AutoSize = true,
            Location = new Point(6, 10),
        };
        page.Controls.Add(_autostart);
        return page;
    }

    // ---- Values ----

    private void LoadValues()
    {
        _workspace.Text = _config.WorkspaceRoot;
        _token.Text = _config.TelegramBotToken;
        _chatId.Text = _config.AllowedChatID;
        _statePath.Text = _config.AgentowerStatePath;
        _apiRoot.Text = _config.TelegramAPIRoot;
        _proxyUrl.Text = _config.TelegramProxyURL;
        // Default to "start at login" on first run so the tray app (and
        // with it the bot) comes up as soon as Windows boots; afterwards
        // reflect whatever the user chose.
        bool firstRun = !File.Exists(AppPaths.SettingsFile);
        _autostart.Checked = firstRun || AutostartManager.IsEnabled;

        foreach (var row in _rows)
        {
            _config.Agents.TryGetValue(row.Kind, out AgentSettings? settings);
            settings ??= new AgentSettings();
            row.Load(settings, _config.Agents.Count == 0);
        }
        RunDetection(updateBinOnlyWhenDefault: true);
    }

    private void RunDetection(bool updateBinOnlyWhenDefault = false)
    {
        var detected = AgentDetector.Detect();
        foreach (var row in _rows)
        {
            if (detected.TryGetValue(row.Kind, out string? path))
            {
                row.SetDetected(path, updateBinOnlyWhenDefault);
            }
            else
            {
                row.SetDetected(null, false);
            }
        }
    }

    private void PickWorkspace()
    {
        using var dialog = new FolderBrowserDialog
        {
            Description = "Elige la carpeta raíz del workspace",
            UseDescriptionForTitle = true,
            SelectedPath = _workspace.Text,
        };
        if (dialog.ShowDialog(this) == DialogResult.OK)
            _workspace.Text = dialog.SelectedPath;
    }

    private void Save()
    {
        var cfg = new BotConfiguration
        {
            WorkspaceRoot = _workspace.Text.Trim(),
            TelegramBotToken = _token.Text.Trim(),
            AllowedChatID = _chatId.Text.Trim(),
            AgentowerStatePath = _statePath.Text.Trim(),
            TelegramAPIRoot = _apiRoot.Text.Trim(),
            TelegramProxyURL = _proxyUrl.Text.Trim(),
            Agents = new Dictionary<string, AgentSettings>(),
        };
        foreach (var row in _rows)
            cfg.Agents[row.Kind] = row.ToSettings();

        string? error = cfg.ValidationMessage;
        if (error != null)
        {
            _message.Text = error;
            return;
        }

        try
        {
            _store.Save(cfg);
            if (_autostart.Checked && !AutostartManager.IsEnabled)
                AutostartManager.Enable();
            else if (!_autostart.Checked && AutostartManager.IsEnabled)
                AutostartManager.Disable();

            Saved = true;
            Close();
        }
        catch (Exception ex)
        {
            _message.Text = ex.Message;
        }
    }

    // ---- Helpers ----

    private static TableLayoutPanel NewTable()
    {
        var table = new TableLayoutPanel
        {
            Dock = DockStyle.Top,
            AutoSize = true,
            ColumnCount = 2,
            Padding = new Padding(0),
        };
        table.ColumnStyles.Add(new ColumnStyle(SizeType.Absolute, 200));
        table.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        return table;
    }

    private static void AddRow(TableLayoutPanel table, string label, Control control)
    {
        int index = table.RowCount++;
        table.RowStyles.Add(new RowStyle(SizeType.Absolute, 34));
        var text = new Label { Text = label, AutoSize = true, Anchor = AnchorStyles.Left, Margin = new Padding(0, 8, 6, 0) };
        control.Margin = new Padding(0, 5, 0, 0);
        table.Controls.Add(text, 0, index);
        table.Controls.Add(control, 1, index);
    }

    /// <summary>One agent row in the Agentes tab.</summary>
    private sealed class AgentRow
    {
        public string Kind { get; }
        public GroupBox Container { get; }
        private readonly CheckBox _enabled;
        private readonly TextBox _bin;
        private readonly NumericUpDown _port;
        private readonly TextBox _args;
        private readonly Label _detected;
        private bool _binIsDefault = true;

        public AgentRow(string kind)
        {
            Kind = kind;
            string label = ConfigStore.DisplayName(kind).ToUpperInvariant();

            Container = new GroupBox
            {
                Text = ConfigStore.DisplayName(kind),
                Width = 545,
                Height = 130,
                Padding = new Padding(10),
            };

            _enabled = new CheckBox { Text = "Habilitado", AutoSize = true, Location = new Point(12, 22) };
            _detected = new Label { AutoSize = true, ForeColor = Color.DimGray, Location = new Point(12, 46), MaximumSize = new Size(515, 0) };

            var binLabel = new Label { Text = $"{label}_BIN", AutoSize = true, Location = new Point(12, 72) };
            _bin = new TextBox { Location = new Point(150, 68), Width = 380 };

            var portLabel = new Label { Text = $"{label}_PORT", AutoSize = true, Location = new Point(12, 100) };
            _port = new NumericUpDown { Location = new Point(150, 96), Width = 90, Minimum = 1, Maximum = 65535 };

            var argsLabel = new Label { Text = $"{label}_ARGS", AutoSize = true, Location = new Point(250, 100) };
            _args = new TextBox { Location = new Point(360, 96), Width = 170, PlaceholderText = "argumentos extra" };

            Container.Controls.Add(_enabled);
            Container.Controls.Add(_detected);
            Container.Controls.Add(binLabel);
            Container.Controls.Add(_bin);
            Container.Controls.Add(portLabel);
            Container.Controls.Add(_port);
            Container.Controls.Add(argsLabel);
            Container.Controls.Add(_args);
        }

        public void Load(AgentSettings settings, bool agentsEmpty)
        {
            _enabled.Checked = settings.Enabled;
            _bin.Text = settings.Bin;
            _port.Value = settings.Port is > 0 and <= 65535 ? settings.Port : ConfigStore.DefaultPort(Kind);
            _args.Text = settings.Args;
            _binIsDefault = string.IsNullOrEmpty(settings.Bin) || settings.Bin == Kind;
        }

        public void SetDetected(string? path, bool updateBinOnlyWhenDefault)
        {
            bool available = Kind == "opencode" || path != null;
            _enabled.Enabled = available;
            if (path != null)
            {
                _detected.Text = "Detectado en " + path;
                if (updateBinOnlyWhenDefault && _binIsDefault)
                {
                    _bin.Text = path;
                    _binIsDefault = false;
                }
            }
            else
            {
                _detected.Text = "No se encontró el binario — instálalo o añádelo al PATH";
            }
        }

        public AgentSettings ToSettings() => new()
        {
            Enabled = _enabled.Checked,
            Bin = _bin.Text.Trim(),
            Port = (int)_port.Value,
            Args = _args.Text.Trim(),
        };
    }
}

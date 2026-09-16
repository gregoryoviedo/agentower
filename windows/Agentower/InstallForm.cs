using System.Windows.Forms;

namespace Agentower;

/// <summary>
/// First-run dialog that offers to install Agentower for the current user.
/// </summary>
internal sealed class InstallForm : Form
{
    private readonly CheckBox _desktop = new();
    private readonly CheckBox _autostart = new();

    public bool DesktopShortcut => _desktop.Checked;
    public bool Autostart => _autostart.Checked;

    public InstallForm(bool alreadyInstalled)
    {
        Text = alreadyInstalled ? "Actualizar Agentower" : "Instalar Agentower";
        Font = new Font("Segoe UI", 9F);
        FormBorderStyle = FormBorderStyle.FixedDialog;
        StartPosition = FormStartPosition.CenterScreen;
        MaximizeBox = false;
        MinimizeBox = false;
        ShowInTaskbar = true;
        ClientSize = new Size(460, 260);

        var title = new Label
        {
            Text = alreadyInstalled ? "Agentower ya está instalado" : "Instalar Agentower",
            Font = new Font("Segoe UI", 12F, FontStyle.Bold),
            AutoSize = true,
            Location = new Point(16, 14),
        };
        var body = new Label
        {
            AutoSize = false,
            Size = new Size(428, 104),
            Location = new Point(16, 48),
            Text = alreadyInstalled
                ? "Ya hay una copia instalada en:\n\n" + Installer.InstallDirectory + "\n\n" +
                  "Pulsa «Reinstalar» para actualizarla con esta versión."
                : "Agentower se copiará a una carpeta estable y quedará listo para " +
                  "arrancar solo sin que tengas que buscar el .exe cada vez:\n\n" +
                  Installer.InstallDirectory,
        };

        _desktop.Text = "Crear acceso directo en el escritorio";
        _desktop.Checked = true;
        _desktop.AutoSize = true;
        _desktop.Location = new Point(18, 158);

        _autostart.Text = "Iniciar Agentower al encender Windows (recomendado)";
        _autostart.Checked = true;
        _autostart.AutoSize = true;
        _autostart.Location = new Point(18, 184);

        var install = new Button
        {
            Text = alreadyInstalled ? "Reinstalar" : "Instalar",
            DialogResult = DialogResult.OK,
            Size = new Size(96, 30),
            Location = new Point(ClientSize.Width - 224, 220),
        };
        var later = new Button
        {
            Text = alreadyInstalled ? "Cancelar" : "Ahora no",
            DialogResult = DialogResult.Cancel,
            Size = new Size(96, 30),
            Location = new Point(ClientSize.Width - 118, 220),
        };

        Controls.Add(title);
        Controls.Add(body);
        Controls.Add(_desktop);
        Controls.Add(_autostart);
        Controls.Add(install);
        Controls.Add(later);
        AcceptButton = install;
        CancelButton = later;
    }
}

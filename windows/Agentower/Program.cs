using System.Threading;
using System.Windows.Forms;

namespace Agentower;

internal static class Program
{
    private const string MutexName = "Agentower.Windows.SingleInstance";

    [STAThread]
    private static void Main()
    {
        ApplicationConfiguration.Initialize();
        string[] args = Environment.GetCommandLineArgs();

        if (args.Contains("--selftest"))
        {
            SelfTest.Run();
            return;
        }

        if (args.Contains("--uninstall"))
        {
            Installer.Uninstall(quiet: args.Contains("--quiet") || args.Contains("--yes"));
            return;
        }

        if (args.Contains("--install"))
        {
            Installer.Install(
                desktopShortcut: !args.Contains("--no-desktop"),
                autostart: !args.Contains("--no-autostart"));
            Installer.StartInstalled();
            return;
        }

        // First run from a portable location: offer to install so the app
        // lives somewhere stable and starts with Windows.
        if (!Installer.IsInstalled && !args.Contains("--portable") && !Installer.PortableMode)
        {
            bool alreadyInstalled = Installer.InstalledCopyExists;
            using var installForm = new InstallForm(alreadyInstalled);
            if (installForm.ShowDialog() == DialogResult.OK)
            {
                Installer.Install(installForm.DesktopShortcut, installForm.Autostart);
                Installer.StartInstalled();
            }
            else if (alreadyInstalled)
            {
                // Already installed: just open the installed copy.
                Installer.StartInstalled();
            }
            else
            {
                Installer.MarkPortable();
            }
            return;
        }

        using Mutex? mutex = AcquireSingleInstance(TimeSpan.FromSeconds(4));
        if (mutex == null)
        {
            MessageBox.Show(
                "Agentower ya está en ejecución. Busca su icono en el área de notificación.",
                "Agentower", MessageBoxButtons.OK, MessageBoxIcon.Information);
            return;
        }

        Application.Run(new TrayApplicationContext());
    }

    /// <summary>
    /// Tries to become the single running instance. The short retry gives
    /// a freshly-installed copy time to take over right after the portable
    /// instance closes itself.
    /// </summary>
    private static Mutex? AcquireSingleInstance(TimeSpan timeout)
    {
        DateTime deadline = DateTime.UtcNow + timeout;
        while (true)
        {
            var mutex = new Mutex(initiallyOwned: true, name: MutexName, out bool createdNew);
            if (createdNew)
                return mutex;
            mutex.Dispose();
            if (DateTime.UtcNow >= deadline)
                return null;
            Thread.Sleep(200);
        }
    }
}

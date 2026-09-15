using System.Threading;
using System.Windows.Forms;

namespace Agentower;

internal static class Program
{
    [STAThread]
    private static void Main()
    {
        using var mutex = new Mutex(initiallyOwned: true, name: "Agentower.Windows.SingleInstance", out bool createdNew);
        if (!createdNew)
        {
            MessageBox.Show(
                "Agentower ya está en ejecución. Busca su icono en el área de notificación.",
                "Agentower",
                MessageBoxButtons.OK,
                MessageBoxIcon.Information);
            return;
        }

        ApplicationConfiguration.Initialize();

        if (Environment.GetCommandLineArgs().Contains("--selftest"))
        {
            SelfTest.Run();
            return;
        }

        Application.Run(new TrayApplicationContext());
    }
}

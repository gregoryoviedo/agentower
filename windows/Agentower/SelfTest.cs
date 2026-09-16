namespace Agentower;

/// <summary>
/// Headless smoke test used by CI/build validation: exercises config
/// load, agent detection and the construction of every form without
/// showing a window or entering the message loop.
/// </summary>
internal static class SelfTest
{
    public static void Run()
    {
        var lines = new List<string>();
        string reportPath = Path.Combine(AppPaths.LogsDirectory, "selftest.log");
        try
        {
            AppPaths.EnsureDirectories();
            var store = new ConfigStore();
            var config = store.Load();
            lines.Add($"config.ok workspace='{config.WorkspaceRoot}' agents={config.Agents.Count}");

            foreach (string kind in ConfigStore.AgentKinds)
            {
                var settings = config.Agents[kind];
                lines.Add($"agent.{kind} enabled={settings.Enabled} available={settings.Available} bin='{settings.Bin}' detected='{settings.DetectedPath ?? "-"}'");
            }

            using (var settingsForm = new SettingsForm(store, config))
            {
                settingsForm.Show();
                Application.DoEvents();
                settingsForm.Hide();
            }
            lines.Add("settingsform.ok");

            using (var installForm = new InstallForm(false))
            {
                installForm.Show();
                Application.DoEvents();
                installForm.Hide();
            }
            lines.Add("installform.ok");

            using (var bot = new BotController())
            using (var idle = new IdleNotifier((_, _) => { }))
            using (var popover = new PopoverForm(
                () => bot.Status, () => config, () => null,
                () => { }, () => { }, () => { }, () => { }))
            {
                popover.Show();
                Application.DoEvents();
                popover.Refresh1();
                popover.Hide();
            }
            lines.Add("popover.ok");
            lines.Add("autostart.enabled=" + AutostartManager.IsEnabled);

            Write(reportPath, "SELFTEST OK", lines);
        }
        catch (Exception ex)
        {
            Write(reportPath, "SELFTEST FAIL", new List<string> { ex.ToString() });
            Environment.ExitCode = 1;
        }
    }

    private static void Write(string reportPath, string header, List<string> lines)
    {
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(reportPath)!);
            var content = new List<string> { header };
            content.AddRange(lines.Select(l => "  " + l));
            File.WriteAllLines(reportPath, content);
        }
        catch
        {
            // ignore; console below is best effort
        }
        Console.WriteLine(header);
        foreach (string line in lines) Console.WriteLine("  " + line);
    }
}

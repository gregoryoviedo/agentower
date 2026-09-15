namespace Agentower;

/// <summary>
/// Windows-side agent detection. Mirrors the Go detector
/// (internal/adapter/agents) but with Windows install locations:
/// %APPDATA%\npm, %USERPROFILE%\.local\bin, ~/.bun, ~/.opencode and the
/// VS Code extension bundles.
/// </summary>
internal static class AgentDetector
{
    private static readonly string[] Extras = BuildExtras();

    public static Dictionary<string, string> Detect()
    {
        var found = new Dictionary<string, string>();

        string? opencode = Which("opencode");
        if (opencode != null) found["opencode"] = opencode;

        string? claude = Which("claude");
        if (claude != null) found["claude"] = claude;

        string? kiro = Which("kiro", "kiro-cli");
        kiro ??= FirstExisting(
            Path.Combine(LocalAppData(), "Programs", "Kiro", "kiro.exe"),
            Path.Combine(Home(), ".kiro", "bin", "kiro.exe"),
            Path.Combine(Home(), ".kiro", "bin", "kiro-cli.exe"));
        if (kiro != null) found["kiro"] = kiro;

        string? copilot = Which("copilot", "copilot-language-server");
        copilot ??= FindCopilotCli();
        copilot ??= FindVSCodeCopilotBundle();
        if (copilot != null) found["copilot"] = copilot;

        string? codex = Which("codex");
        codex ??= FirstExisting(
            Path.Combine(AppData(), "npm", "codex.cmd"),
            Path.Combine(Home(), ".codex", "bin", "codex.exe"));
        if (codex != null) found["codex"] = codex;

        string? antigravity = Which("agy");
        antigravity ??= FirstExisting(
            Path.Combine(AppData(), "npm", "agy.cmd"),
            Path.Combine(Home(), ".local", "bin", "agy.exe"));
        if (antigravity != null) found["antigravity"] = antigravity;

        return found;
    }

    private static string? Which(params string[] names)
    {
        var dirs = SearchDirs();
        var exts = PathExts();
        foreach (string name in names)
        {
            if (Path.IsPathRooted(name) && File.Exists(name))
                return name;
            foreach (string dir in dirs)
            {
                foreach (string ext in exts)
                {
                    string candidate = Path.Combine(dir, name + ext);
                    if (File.Exists(candidate))
                        return candidate;
                }
            }
        }
        return null;
    }

    private static List<string> SearchDirs()
    {
        var dirs = new List<string>();
        void Add(string? dir)
        {
            if (!string.IsNullOrWhiteSpace(dir) && !dirs.Contains(dir, StringComparer.OrdinalIgnoreCase))
                dirs.Add(dir!);
        }

        foreach (string dir in (Environment.GetEnvironmentVariable("PATH") ?? "").Split(';'))
            Add(dir.Trim());
        foreach (string dir in Extras)
            Add(dir);
        return dirs.Where(Directory.Exists).ToList();
    }

    private static string[] PathExts()
    {
        string raw = Environment.GetEnvironmentVariable("PATHEXT") ?? ".COM;.EXE;.BAT;.CMD";
        var exts = new List<string> { "" };
        foreach (string e in raw.Split(';'))
            if (!string.IsNullOrWhiteSpace(e))
                exts.Add(e.Trim().ToLowerInvariant());
        return exts.ToArray();
    }

    private static string[] BuildExtras()
    {
        var extras = new List<string>
        {
            Path.Combine(Home(), ".local", "bin"),
            Path.Combine(Home(), ".bun", "bin"),
            Path.Combine(Home(), ".opencode", "bin"),
            Path.Combine(Home(), ".codex", "bin"),
            Path.Combine(Home(), ".npm-global", "bin"),
        };
        string appData = AppData();
        if (appData.Length > 0) extras.Add(Path.Combine(appData, "npm"));
        string local = LocalAppData();
        if (local.Length > 0) extras.Add(Path.Combine(local, "Programs"));
        string pf = Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles);
        if (pf.Length > 0) extras.Add(Path.Combine(pf, "nodejs"));
        return extras.ToArray();
    }

    private static string? FindCopilotCli()
    {
        string? hit = Which("copilot");
        if (hit != null) return hit;
        string appData = AppData();
        return FirstExisting(
            appData.Length > 0 ? Path.Combine(appData, "npm", "copilot.cmd") : null,
            Path.Combine(Home(), ".bun", "bin", "copilot.exe"));
    }

    /// <summary>
    /// Scans %USERPROFILE%\.vscode\extensions for the newest installed
    /// github.copilot* extension and returns its dist/extension.js.
    /// </summary>
    private static string? FindVSCodeCopilotBundle()
    {
        string root = Path.Combine(Home(), ".vscode", "extensions");
        if (!Directory.Exists(root)) return null;
        try
        {
            var matches = Directory.GetDirectories(root)
                .Where(d => Path.GetFileName(d).StartsWith("github.copilot-", StringComparison.OrdinalIgnoreCase)
                         || Path.GetFileName(d).StartsWith("github.copilot-chat-", StringComparison.OrdinalIgnoreCase))
                .OrderBy(d => Path.GetFileName(d), StringComparer.OrdinalIgnoreCase)
                .ToList();
            for (int i = matches.Count - 1; i >= 0; i--)
            {
                string bundle = Path.Combine(matches[i], "dist", "extension.js");
                if (File.Exists(bundle)) return bundle;
            }
        }
        catch
        {
            // best effort
        }
        return null;
    }

    private static string? FirstExisting(params string?[] paths)
    {
        foreach (string? p in paths)
        {
            if (!string.IsNullOrEmpty(p) && File.Exists(p))
                return p;
        }
        return null;
    }

    private static string Home() => Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
    private static string AppData() => Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData);
    private static string LocalAppData() => Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
}

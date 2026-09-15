namespace Agentower;

/// <summary>Loads the tray/application icon embedded in the assembly.</summary>
internal static class IconFactory
{
    public static Icon LoadAppIcon()
    {
        var asm = typeof(IconFactory).Assembly;
        using var stream = asm.GetManifestResourceStream("Agentower.Resources.agentower.ico");
        if (stream != null)
        {
            using var ms = new MemoryStream();
            stream.CopyTo(ms);
            ms.Position = 0;
            try
            {
                return new Icon(ms);
            }
            catch
            {
                // fall through to the system icon
            }
        }
        return SystemIcons.Application;
    }

    public static Bitmap? LoadMenuBitmap()
    {
        var asm = typeof(IconFactory).Assembly;
        using var stream = asm.GetManifestResourceStream("Agentower.Resources.menubar-icon.png");
        if (stream == null) return null;
        try
        {
            return new Bitmap(stream);
        }
        catch
        {
            return null;
        }
    }
}

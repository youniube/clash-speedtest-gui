using System;
using System.Diagnostics;
using System.IO;

namespace ClashSpeedTestGUI.UiFixtures
{
    internal static class FixtureLauncher
    {
        private static int Main()
        {
            try
            {
                string root = AppDomain.CurrentDomain.BaseDirectory.TrimEnd(
                    Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
                string guiPath = Path.Combine(root, "Clash-SpeedTest-GUI.exe");
                if (!File.Exists(guiPath))
                    throw new FileNotFoundException("UI fixture GUI is missing.", guiPath);

                string profile = EnsureDirectory(Path.Combine(root, "profile"));
                string temporary = EnsureDirectory(Path.Combine(root, "temp"));
                EnsureDirectory(Path.Combine(root, "control"));
                EnsureDirectory(Path.Combine(root, "signals"));
                EnsureDirectory(Path.Combine(root, "work"));

                ProcessStartInfo startInfo = new ProcessStartInfo
                {
                    FileName = guiPath,
                    WorkingDirectory = root,
                    UseShellExecute = false
                };
                startInfo.EnvironmentVariables[
                    "CLASH_SPEEDTEST_GUI_SETTINGS_DIRECTORY"] = profile;
                startInfo.EnvironmentVariables[
                    "CLASH_SPEEDTEST_GUI_UI_FIXTURE_ROOT"] = root;
                startInfo.EnvironmentVariables["TEMP"] = temporary;
                startInfo.EnvironmentVariables["TMP"] = temporary;
                Process process = Process.Start(startInfo);
                if (process == null) throw new InvalidOperationException("UI fixture GUI did not start.");
                return 0;
            }
            catch (Exception ex)
            {
                Console.Error.WriteLine(ex.Message);
                return 1;
            }
        }

        private static string EnsureDirectory(string path)
        {
            Directory.CreateDirectory(path);
            return path;
        }
    }
}

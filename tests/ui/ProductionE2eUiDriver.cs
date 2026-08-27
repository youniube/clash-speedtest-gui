using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;
using FlaUI.Core.Conditions;
using FlaUI.UIA3;

namespace ClashSpeedTestGUI.UiFixtures
{
    internal static class ProductionE2eUiDriver
    {
        private const int TimeoutMilliseconds = 20000;
        private const int WindowMessageClose = 0x0010;
        private const int WindowMessageKeyDown = 0x0100;
        private const int WindowMessageKeyUp = 0x0101;
        private const int VirtualKeyF5 = 0x74;

        [STAThread]
        private static int Main(string[] args)
        {
            Process guiProcess = null;
            PipelineArtifactObserver observer = null;
            LocalSocksProbeServer firstServer = null;
            LocalSocksProbeServer secondServer = null;
            try
            {
                DriverOptions options = DriverOptions.Parse(args);
                byte[] guardSnapshot = File.ReadAllBytes(options.GuardOutputPath);

                firstServer = new LocalSocksProbeServer();
                secondServer = new LocalSocksProbeServer();
                MaterializeSuccessInput(
                    options.SuccessInputPath, firstServer.Port, secondServer.Port);

                StartLauncher(options.LauncherPath);
                guiProcess = WaitForGuiProcess(options.SandboxPath);
                observer = new PipelineArtifactObserver(options.SandboxPath);

                using (UIA3Automation automation = new UIA3Automation())
                {
                    automation.ConnectionTimeout = TimeSpan.FromSeconds(3);
                    automation.TransactionTimeout = TimeSpan.FromSeconds(3);
                    Window mainWindow = WaitForWindow(
                        automation, guiProcess.Id, "MainWindow", null);
                    mainWindow.SetForeground();
                    AutomationElement startButton = FindById(
                        mainWindow, "StartSpeedTestButton");
                    if (startButton == null)
                        throw new InvalidOperationException("Start button was not found.");

                    PipelineArtifactCounts beforeSuccess = observer.Snapshot();
                    PostKey(guiProcess.MainWindowHandle, VirtualKeyF5);
                    string completionText = WaitForDialogText(
                        automation, guiProcess.Id, "完成");
                    AssertContains(completionText, "测速完成：2/2，有效 2 个，失败 0 个",
                        "successful completion dialog");
                    DismissDialog(guiProcess.Id, "完成");
                    WaitForEnabled(startButton, "successful production run did not return to idle");
                    WaitForArtifactDeltas(observer, beforeSuccess, 1, 1, 1, 1,
                        "successful production run");
                    AssertElementTextContains(mainWindow, "StatisticsText",
                        "总数 2 | 筛选后 2");
                    AssertElementTextContains(mainWindow, "StatisticsText",
                        "有效 2 | 失败 0 | 等待 0");
                    AssertSuccessOutput(
                        options.SuccessOutputPath, firstServer.Port, secondServer.Port);
                    AssertTaskTempClean(options.SandboxPath);
                    Console.WriteLine("Successful production flow passed.");

                    mainWindow.SetForeground();
                    SetText(mainWindow, "ConfigSourceInput", options.InvalidInputPath);
                    SetText(mainWindow, "OutputPathInput", options.GuardOutputPath);
                    PipelineArtifactCounts beforeInvalid = observer.Snapshot();
                    startButton.AsButton().Invoke();
                    string invalidText = WaitForDialogText(
                        automation, guiProcess.Id, "处理失败");
                    AssertContains(invalidText, "line 2", "invalid URI failure dialog");
                    AssertNotContains(invalidText, "partial-secret",
                        "invalid URI failure dialog");
                    DismissDialog(guiProcess.Id, "处理失败");
                    WaitForEnabled(startButton, "invalid URI run did not return to idle");
                    WaitForArtifactDeltas(observer, beforeInvalid, 0, 0, 0, 0,
                        "invalid URI run");
                    AssertFileUnchanged(options.GuardOutputPath, guardSnapshot);
                    AssertTaskTempClean(options.SandboxPath);
                    Console.WriteLine("Invalid URI fail-closed flow passed.");

                    mainWindow.SetForeground();
                    SetText(mainWindow, "ConfigSourceInput", options.RegexInputPath);
                    PipelineArtifactCounts beforeRegex = observer.Snapshot();
                    Stopwatch regexTimer = Stopwatch.StartNew();
                    startButton.AsButton().Invoke();
                    WaitForWindow(automation, guiProcess.Id, null, "确认覆盖");
                    DismissDialog(guiProcess.Id, "确认覆盖");
                    string regexText = WaitForDialogText(
                        automation, guiProcess.Id, "测速失败");
                    regexTimer.Stop();
                    AssertContains(regexText, "load proxies failed",
                        "Provider regexp failure dialog");
                    AssertContains(regexText.ToLowerInvariant(), "timeout",
                        "Provider regexp failure dialog");
                    AssertNotContains(regexText, "provider-timeout-node-secret",
                        "Provider regexp failure dialog");
                    if (regexTimer.Elapsed > TimeSpan.FromSeconds(5))
                    {
                        throw new InvalidOperationException(
                            "Provider regexp failure was not bounded: " + regexTimer.Elapsed + ".");
                    }
                    DismissDialog(guiProcess.Id, "测速失败");
                    WaitForEnabled(startButton, "Provider regexp run did not return to idle");
                    WaitForArtifactDeltas(observer, beforeRegex, 1, 1, 1, 0,
                        "Provider regexp run");
                    AssertFileUnchanged(options.GuardOutputPath, guardSnapshot);
                    AssertTaskTempClean(options.SandboxPath);
                    Console.WriteLine("Provider regexp fail-closed flow passed.");

                    PipelineArtifactCounts finalCounts = observer.Snapshot();
                    Console.WriteLine("Production GUI -> parser -> runner flow passed.");
                    Console.WriteLine(
                        "Observed pipeline artifacts: parser={0}, requests={1}, prepared={2}, core-outputs={3}.",
                        finalCounts.ParserOutputs, finalCounts.PreparationRequests,
                        finalCounts.MaterializedConfigs, finalCounts.CoreOutputs);
                    Console.WriteLine("Provider regexp failure elapsed: {0:0.000}s.",
                        regexTimer.Elapsed.TotalSeconds);
                }
                return 0;
            }
            catch (Exception ex)
            {
                Console.Error.WriteLine(ex.ToString());
                return 1;
            }
            finally
            {
                if (guiProcess != null)
                {
                    try
                    {
                        if (!guiProcess.HasExited && guiProcess.MainWindowHandle != IntPtr.Zero)
                            PostMessage(guiProcess.MainWindowHandle,
                                WindowMessageClose, IntPtr.Zero, IntPtr.Zero);
                        if (!guiProcess.WaitForExit(3000)) guiProcess.Kill();
                    }
                    catch { }
                    guiProcess.Dispose();
                }
                if (observer != null) observer.Dispose();
                if (firstServer != null) firstServer.Dispose();
                if (secondServer != null) secondServer.Dispose();
            }
        }

        private static void MaterializeSuccessInput(string path, int firstPort, int secondPort)
        {
            string value = File.ReadAllText(path, Encoding.UTF8)
                .Replace("__PORT1__", firstPort.ToString())
                .Replace("__PORT2__", secondPort.ToString());
            if (value.IndexOf("__PORT", StringComparison.Ordinal) >= 0)
                throw new InvalidOperationException("Success input still contains port placeholders.");
            File.WriteAllText(path, value, new UTF8Encoding(false));
        }

        private static void AssertSuccessOutput(string path, int firstPort, int secondPort)
        {
            if (!File.Exists(path))
                throw new FileNotFoundException("Successful run did not commit an output.", path);
            string yaml = File.ReadAllText(path, Encoding.UTF8);
            if (CountOccurrences(yaml, "name:") != 2
                || yaml.IndexOf("[p] shared", StringComparison.Ordinal) < 0
                || yaml.IndexOf("[p] shared [2]", StringComparison.Ordinal) < 0
                || yaml.IndexOf("port: " + firstPort, StringComparison.Ordinal) < 0
                || yaml.IndexOf("port: " + secondPort, StringComparison.Ordinal) < 0)
            {
                throw new InvalidOperationException(
                    "Successful output did not preserve both raw Provider duplicates.\n" + yaml);
            }
        }

        private static int CountOccurrences(string value, string fragment)
        {
            int count = 0;
            int offset = 0;
            while ((offset = value.IndexOf(fragment, offset, StringComparison.Ordinal)) >= 0)
            {
                count++;
                offset += fragment.Length;
            }
            return count;
        }

        private static void AssertFileUnchanged(string path, byte[] expected)
        {
            byte[] actual = File.ReadAllBytes(path);
            if (!actual.SequenceEqual(expected))
                throw new InvalidOperationException("Guard output was modified: " + path);
        }

        private static void AssertTaskTempClean(string sandboxPath)
        {
            string taskRoot = Path.Combine(sandboxPath, "temp", "ClashSpeedTestGUI");
            if (Directory.Exists(taskRoot)
                && Directory.EnumerateFileSystemEntries(taskRoot).Any())
            {
                throw new InvalidOperationException(
                    "GUI task temporary directory was not cleaned: " + taskRoot);
            }
            string work = Path.Combine(sandboxPath, "work");
            if (Directory.EnumerateFiles(work, "*.cstgui-*.tmp.yaml").Any())
                throw new InvalidOperationException("GUI left a core output temporary file.");
        }

        private static void WaitForArtifactDeltas(PipelineArtifactObserver observer,
            PipelineArtifactCounts before, int expectedParserOutputs,
            int expectedPreparationRequests, int expectedMaterializedConfigs,
            int expectedCoreOutputs, string scenario)
        {
            if (expectedParserOutputs > 0 || expectedPreparationRequests > 0
                || expectedMaterializedConfigs > 0 || expectedCoreOutputs > 0)
            {
                WaitUntil(delegate
                {
                    PipelineArtifactCounts current = observer.Snapshot();
                    return current.ParserOutputs - before.ParserOutputs >= expectedParserOutputs
                        && current.PreparationRequests - before.PreparationRequests
                            >= expectedPreparationRequests
                        && current.MaterializedConfigs - before.MaterializedConfigs
                            >= expectedMaterializedConfigs
                        && current.CoreOutputs - before.CoreOutputs >= expectedCoreOutputs;
                }, scenario + " pipeline artifacts were not observed");
            }
            Thread.Sleep(500);
            PipelineArtifactCounts after = observer.Snapshot();
            int parserDelta = after.ParserOutputs - before.ParserOutputs;
            int requestDelta = after.PreparationRequests - before.PreparationRequests;
            int materializedDelta = after.MaterializedConfigs - before.MaterializedConfigs;
            int coreDelta = after.CoreOutputs - before.CoreOutputs;
            if (parserDelta != expectedParserOutputs
                || requestDelta != expectedPreparationRequests
                || materializedDelta != expectedMaterializedConfigs
                || coreDelta != expectedCoreOutputs)
            {
                throw new InvalidOperationException(string.Format(
                    "{0} artifacts were parser={1}, requests={2}, prepared={3}, core-outputs={4}; "
                    + "expected parser={5}, requests={6}, prepared={7}, core-outputs={8}.",
                    scenario, parserDelta, requestDelta, materializedDelta, coreDelta,
                    expectedParserOutputs, expectedPreparationRequests,
                    expectedMaterializedConfigs, expectedCoreOutputs));
            }
        }

        private static void StartLauncher(string launcherPath)
        {
            ProcessStartInfo startInfo = new ProcessStartInfo
            {
                FileName = launcherPath,
                WorkingDirectory = Path.GetDirectoryName(launcherPath),
                UseShellExecute = false,
                CreateNoWindow = true
            };
            using (Process launcher = Process.Start(startInfo))
            {
                if (launcher == null)
                    throw new InvalidOperationException("Production E2E launcher did not start.");
                if (!launcher.WaitForExit(5000))
                    throw new TimeoutException("Production E2E launcher did not exit.");
                if (launcher.ExitCode != 0)
                    throw new InvalidOperationException(
                        "Production E2E launcher failed with exit code " + launcher.ExitCode + ".");
            }
        }

        private static Process WaitForGuiProcess(string sandboxPath)
        {
            string expected = Path.GetFullPath(
                Path.Combine(sandboxPath, "Clash-SpeedTest-GUI.exe"));
            Process result = null;
            WaitUntil(delegate
            {
                foreach (Process process in Process.GetProcessesByName("Clash-SpeedTest-GUI"))
                {
                    try
                    {
                        string actual = Path.GetFullPath(process.MainModule.FileName);
                        if (string.Equals(actual, expected, StringComparison.OrdinalIgnoreCase))
                        {
                            result = process;
                            return process.MainWindowHandle != IntPtr.Zero;
                        }
                    }
                    catch
                    {
                        process.Dispose();
                    }
                }
                return false;
            }, "production E2E GUI process did not expose a window");
            return result;
        }

        private static Window WaitForWindow(
            UIA3Automation automation, int processId, string automationId, string name)
        {
            Window result = null;
            WaitUntil(delegate
            {
                ConditionFactory factory = automation.ConditionFactory;
                ConditionBase condition = factory.ByProcessId(processId);
                if (!string.IsNullOrEmpty(automationId))
                    condition = condition.And(factory.ByAutomationId(automationId));
                if (!string.IsNullOrEmpty(name))
                    condition = condition.And(factory.ByName(name));
                AutomationElement element = automation.GetDesktop().FindFirstDescendant(condition);
                if (element == null) return false;
                result = element.AsWindow();
                return result != null && result.IsAvailable;
            }, "window was not found: " + (automationId ?? name));
            return result;
        }

        private static string WaitForDialogText(
            UIA3Automation automation, int processId, string title)
        {
            Window dialog = WaitForWindow(automation, processId, null, title);
            List<string> parts = new List<string> { dialog.Name ?? "" };
            parts.AddRange(dialog.FindAllDescendants()
                .Select(delegate(AutomationElement item) { return item.Name ?? ""; })
                .Where(delegate(string value) { return value.Length > 0; }));
            return string.Join("\n", parts.ToArray());
        }

        private static void DismissDialog(int processId, string title)
        {
            IntPtr dialog = FindNativeWindow(processId, title);
            if (dialog == IntPtr.Zero)
                throw new InvalidOperationException("Dialog was not found: " + title);
            PostKey(dialog, 0x0D);
            WaitUntil(delegate
            {
                return FindNativeWindow(processId, title) == IntPtr.Zero;
            }, "dialog did not close: " + title);
        }

        private static IntPtr FindNativeWindow(int processId, string title)
        {
            IntPtr handle = FindWindow(null, title);
            if (handle == IntPtr.Zero || !IsWindowVisible(handle)) return IntPtr.Zero;
            uint ownerProcess;
            GetWindowThreadProcessId(handle, out ownerProcess);
            return ownerProcess == (uint)processId ? handle : IntPtr.Zero;
        }

        private static AutomationElement FindById(AutomationElement root, string automationId)
        {
            return root.FindFirstDescendant(delegate(ConditionFactory factory)
            {
                return factory.ByAutomationId(automationId);
            });
        }

        private static void SetText(AutomationElement root, string automationId, string value)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null)
                throw new InvalidOperationException("Text input was not found: " + automationId);
            element.AsTextBox().Text = value;
        }

        private static void AssertElementTextContains(
            AutomationElement root, string automationId, string expected)
        {
            if (!ElementTextContains(root, automationId, expected))
                throw new InvalidOperationException(
                    automationId + " did not contain '" + expected + "'.");
        }

        private static bool ElementTextContains(
            AutomationElement root, string automationId, string expected)
        {
            AutomationElement element = FindById(root, automationId);
            if (element != null && (element.Name ?? "")
                .IndexOf(expected, StringComparison.Ordinal) >= 0) return true;
            if (element != null && element.FindAllDescendants().Any(
                delegate(AutomationElement child)
                {
                    return (child.Name ?? "").IndexOf(
                        expected, StringComparison.Ordinal) >= 0;
                })) return true;
            AutomationElement statusStrip = FindById(root, "StatusStrip");
            if (statusStrip == null) return false;
            if ((statusStrip.Name ?? "").IndexOf(expected, StringComparison.Ordinal) >= 0)
                return true;
            return statusStrip.FindAllDescendants().Any(
                delegate(AutomationElement child)
                {
                    return (child.Name ?? "").IndexOf(
                        expected, StringComparison.Ordinal) >= 0;
                });
        }

        private static void WaitForEnabled(AutomationElement element, string failure)
        {
            WaitUntil(delegate
            {
                return element != null && element.IsAvailable
                    && element.Properties.IsEnabled.Value;
            }, failure);
        }

        private static void WaitUntil(Func<bool> condition, string failure)
        {
            Stopwatch stopwatch = Stopwatch.StartNew();
            Exception lastError = null;
            while (stopwatch.ElapsedMilliseconds < TimeoutMilliseconds)
            {
                try
                {
                    if (condition()) return;
                }
                catch (Exception ex)
                {
                    lastError = ex;
                }
                Thread.Sleep(50);
            }
            throw new TimeoutException(failure,
                lastError == null ? null : lastError);
        }

        private static void AssertContains(string value, string expected, string label)
        {
            if ((value ?? "").IndexOf(expected, StringComparison.Ordinal) < 0)
                throw new InvalidOperationException(
                    label + " did not contain '" + expected + "'. Actual: " + value);
        }

        private static void AssertNotContains(string value, string unexpected, string label)
        {
            if ((value ?? "").IndexOf(unexpected, StringComparison.Ordinal) >= 0)
                throw new InvalidOperationException(
                    label + " leaked '" + unexpected + "'. Actual: " + value);
        }

        private static void PostKey(IntPtr handle, int virtualKey)
        {
            PostMessage(handle, WindowMessageKeyDown,
                new IntPtr(virtualKey), new IntPtr(1));
            Thread.Sleep(40);
            PostMessage(handle, WindowMessageKeyUp,
                new IntPtr(virtualKey), new IntPtr(unchecked((int)0xC0000001)));
            Thread.Sleep(40);
        }

        [System.Runtime.InteropServices.DllImport("user32.dll", SetLastError = true)]
        private static extern IntPtr FindWindow(string className, string windowName);

        [System.Runtime.InteropServices.DllImport("user32.dll")]
        private static extern bool IsWindowVisible(IntPtr hWnd);

        [System.Runtime.InteropServices.DllImport("user32.dll")]
        private static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);

        [System.Runtime.InteropServices.DllImport("user32.dll", SetLastError = true)]
        private static extern bool PostMessage(
            IntPtr hWnd, int message, IntPtr wParam, IntPtr lParam);

        private sealed class PipelineArtifactObserver : IDisposable
        {
            private readonly object sync = new object();
            private readonly FileSystemWatcher watcher;
            private readonly HashSet<string> parserOutputs =
                new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            private readonly HashSet<string> preparationRequests =
                new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            private readonly HashSet<string> materializedConfigs =
                new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            private readonly HashSet<string> coreOutputs =
                new HashSet<string>(StringComparer.OrdinalIgnoreCase);

            public PipelineArtifactObserver(string sandboxPath)
            {
                string taskRoot = Path.Combine(sandboxPath, "temp", "ClashSpeedTestGUI");
                Directory.CreateDirectory(taskRoot);
                watcher = new FileSystemWatcher(sandboxPath)
                {
                    IncludeSubdirectories = true,
                    NotifyFilter = NotifyFilters.FileName | NotifyFilters.DirectoryName
                };
                watcher.Created += HandleEvent;
                watcher.Renamed += HandleRenamedEvent;
                watcher.EnableRaisingEvents = true;
            }

            public PipelineArtifactCounts Snapshot()
            {
                lock (sync)
                {
                    return new PipelineArtifactCounts(
                        parserOutputs.Count, preparationRequests.Count,
                        materializedConfigs.Count, coreOutputs.Count);
                }
            }

            private void HandleEvent(object sender, FileSystemEventArgs args)
            {
                Record(args.FullPath);
            }

            private void HandleRenamedEvent(object sender, RenamedEventArgs args)
            {
                Record(args.FullPath);
            }

            private void Record(string path)
            {
                string name = Path.GetFileName(path);
                lock (sync)
                {
                    if (name.StartsWith("config-", StringComparison.OrdinalIgnoreCase)
                        && name.EndsWith(".yaml", StringComparison.OrdinalIgnoreCase))
                        parserOutputs.Add(path);
                    if (string.Equals(name, "source-preparation-request.json",
                        StringComparison.OrdinalIgnoreCase)) preparationRequests.Add(path);
                    if (name.StartsWith("materialized-", StringComparison.OrdinalIgnoreCase)
                        && name.EndsWith(".yaml", StringComparison.OrdinalIgnoreCase))
                        materializedConfigs.Add(path);
                    if (name.IndexOf(".cstgui-", StringComparison.OrdinalIgnoreCase) >= 0
                        && name.EndsWith(".tmp.yaml", StringComparison.OrdinalIgnoreCase))
                        coreOutputs.Add(path);
                }
            }

            public void Dispose()
            {
                watcher.EnableRaisingEvents = false;
                watcher.Dispose();
            }
        }

        private struct PipelineArtifactCounts
        {
            public readonly int ParserOutputs;
            public readonly int PreparationRequests;
            public readonly int MaterializedConfigs;
            public readonly int CoreOutputs;

            public PipelineArtifactCounts(
                int parserOutputs, int preparationRequests,
                int materializedConfigs, int coreOutputs)
            {
                ParserOutputs = parserOutputs;
                PreparationRequests = preparationRequests;
                MaterializedConfigs = materializedConfigs;
                CoreOutputs = coreOutputs;
            }
        }

        private sealed class LocalSocksProbeServer : IDisposable
        {
            private readonly TcpListener listener;
            private readonly Thread acceptThread;
            private volatile bool stopping;

            public int Port { get; private set; }

            public LocalSocksProbeServer()
            {
                listener = new TcpListener(IPAddress.Loopback, 0);
                listener.Start();
                Port = ((IPEndPoint)listener.LocalEndpoint).Port;
                acceptThread = new Thread(AcceptLoop) { IsBackground = true };
                acceptThread.Start();
            }

            private void AcceptLoop()
            {
                while (!stopping)
                {
                    try
                    {
                        TcpClient client = listener.AcceptTcpClient();
                        ThreadPool.QueueUserWorkItem(delegate { HandleClient(client); });
                    }
                    catch (SocketException)
                    {
                        if (!stopping) throw;
                    }
                    catch (ObjectDisposedException)
                    {
                        return;
                    }
                }
            }

            private static void HandleClient(TcpClient client)
            {
                using (client)
                using (NetworkStream stream = client.GetStream())
                {
                    client.ReceiveTimeout = 5000;
                    client.SendTimeout = 5000;
                    if (ReadByte(stream) != 5) return;
                    int methodCount = ReadByte(stream);
                    if (methodCount <= 0) return;
                    ReadExact(stream, methodCount);
                    stream.Write(new byte[] { 5, 0 }, 0, 2);

                    byte[] request = ReadExact(stream, 4);
                    if (request[0] != 5 || request[1] != 1) return;
                    int addressLength;
                    if (request[3] == 1) addressLength = 4;
                    else if (request[3] == 4) addressLength = 16;
                    else if (request[3] == 3) addressLength = ReadByte(stream);
                    else return;
                    ReadExact(stream, addressLength + 2);
                    byte[] connected = new byte[] { 5, 0, 0, 1, 127, 0, 0, 1, 0, 0 };
                    stream.Write(connected, 0, connected.Length);
                    stream.Flush();

                    ReadHttpHeader(stream);
                    byte[] response = Encoding.ASCII.GetBytes(
                        "HTTP/1.1 200 OK\r\nContent-Length: 1\r\n"
                        + "Content-Type: application/octet-stream\r\n"
                        + "Connection: close\r\n\r\nx");
                    stream.Write(response, 0, response.Length);
                    stream.Flush();
                }
            }

            private static int ReadByte(Stream stream)
            {
                int value = stream.ReadByte();
                if (value < 0) throw new EndOfStreamException();
                return value;
            }

            private static byte[] ReadExact(Stream stream, int count)
            {
                byte[] result = new byte[count];
                int offset = 0;
                while (offset < count)
                {
                    int read = stream.Read(result, offset, count - offset);
                    if (read <= 0) throw new EndOfStreamException();
                    offset += read;
                }
                return result;
            }

            private static void ReadHttpHeader(Stream stream)
            {
                int matched = 0;
                byte[] marker = new byte[] { 13, 10, 13, 10 };
                for (int count = 0; count < 16384; count++)
                {
                    int value = ReadByte(stream);
                    matched = value == marker[matched] ? matched + 1 : value == 13 ? 1 : 0;
                    if (matched == marker.Length) return;
                }
                throw new InvalidDataException("HTTP request header exceeded the fixture limit.");
            }

            public void Dispose()
            {
                stopping = true;
                listener.Stop();
                if (acceptThread.IsAlive) acceptThread.Join(1000);
            }
        }

        private sealed class DriverOptions
        {
            public string SandboxPath;
            public string LauncherPath;
            public string SuccessInputPath;
            public string InvalidInputPath;
            public string RegexInputPath;
            public string SuccessOutputPath;
            public string GuardOutputPath;

            public static DriverOptions Parse(string[] args)
            {
                Dictionary<string, string> values = new Dictionary<string, string>(
                    StringComparer.OrdinalIgnoreCase);
                for (int index = 0; index < args.Length; index += 2)
                {
                    if (index + 1 >= args.Length || !args[index].StartsWith("--"))
                        throw new ArgumentException("Driver arguments must be --name value pairs.");
                    values[args[index]] = Path.GetFullPath(args[index + 1]);
                }
                string[] required = new[]
                {
                    "--sandbox", "--launcher", "--success-input", "--invalid-input",
                    "--regex-input", "--success-output", "--guard-output"
                };
                foreach (string key in required)
                {
                    if (!values.ContainsKey(key))
                        throw new ArgumentException("Missing driver argument: " + key);
                }
                return new DriverOptions
                {
                    SandboxPath = values["--sandbox"],
                    LauncherPath = values["--launcher"],
                    SuccessInputPath = values["--success-input"],
                    InvalidInputPath = values["--invalid-input"],
                    RegexInputPath = values["--regex-input"],
                    SuccessOutputPath = values["--success-output"],
                    GuardOutputPath = values["--guard-output"]
                };
            }
        }
    }
}

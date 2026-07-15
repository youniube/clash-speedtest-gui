using System;
using System.Diagnostics;
using System.Drawing;
using System.Drawing.Imaging;
using System.IO;
using System.Runtime.InteropServices;
using System.Threading;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;
using FlaUI.Core.Conditions;
using FlaUI.Core.Input;
using FlaUI.Core.WindowsAPI;
using FlaUI.UIA3;

namespace ClashSpeedTestGUI.UiFixtures
{
    internal static class WinFormsUiDriver
    {
        private const int TimeoutMilliseconds = 15000;
        private const uint PrintWindowRenderFullContent = 0x00000002;
        private const int SourceCopy = 0x00CC0020;
        private const int CaptureBlt = 0x40000000;
        private const int WindowMessageClose = 0x0010;
        private const int WindowMessageKeyDown = 0x0100;
        private const int WindowMessageKeyUp = 0x0101;
        private const int ButtonMessageClick = 0x00F5;
        private const int VirtualKeyEscape = 0x1B;
        private const int VirtualKeyEnd = 0x23;
        private const int VirtualKeyF5 = 0x74;
        private const int VirtualKeyF6 = 0x75;
        private static string tracePath;

        [STAThread]
        private static int Main(string[] args)
        {
            Process guiProcess = null;
            Window mainWindow = null;
            System.Windows.Forms.DataObject clipboardSnapshot = null;
            bool clipboardCaptured = false;
            try
            {
                clipboardSnapshot = CaptureClipboardSnapshot();
                clipboardCaptured = true;
                DriverOptions options = DriverOptions.Parse(args);
                tracePath = Path.Combine(options.SandboxPath, "work", "driver-trace.log");
                Trace("options parsed");
                StartLauncher(options.LauncherPath);
                Trace("launcher completed");
                guiProcess = WaitForGuiProcess(options.SandboxPath);
                Trace("GUI process found");

                using (UIA3Automation automation = new UIA3Automation())
                {
                    automation.ConnectionTimeout = TimeSpan.FromSeconds(3);
                    automation.TransactionTimeout = TimeSpan.FromSeconds(3);
                    mainWindow = WaitForWindow(
                        automation, guiProcess.Id, "MainWindow", null);
                    Trace("main window found");
                    mainWindow.SetForeground();
                    AutomationElement resultGrid = FindById(mainWindow, "ResultGrid");
                    if (resultGrid == null)
                        throw new InvalidOperationException("Result grid was not found.");
                    AutomationElement startButton = FindById(
                        mainWindow, "StartSpeedTestButton");
                    if (startButton == null)
                        throw new InvalidOperationException("Start button was not found.");

                    SetText(mainWindow, "ConfigSourceInput", options.InputPath);
                    SetText(mainWindow, "OutputPathInput", options.OutputPath);
                    Trace("inputs set");

                    WriteControl(options.SandboxPath, "speed-mode.txt", "gated-success");
                    DeleteSignal(options.SandboxPath, "speed-started.json");
                    DeleteSignal(options.SandboxPath, "speed-completed.json");
                    PostKey(guiProcess.MainWindowHandle, VirtualKeyF5);
                    WaitForSignal(options.SandboxPath, "speed-started.json");
                    Trace("gated speed test started through F5");
                    AssertEnabled(mainWindow, "ConfigSourceInput", false);
                    AssertEnabled(mainWindow, "OutputPathInput", false);
                    AssertEnabled(mainWindow, "SpeedPresetCombo", false);
                    AssertEnabled(mainWindow, "StartSpeedTestButton", false);
                    AssertEnabled(mainWindow, "StopTaskButton", true);
                    PostKey(guiProcess.MainWindowHandle, VirtualKeyEscape);
                    WaitForEnabled(startButton, "Esc did not return the speed test UI to idle");
                    WaitForDisabled(FindById(mainWindow, "StopTaskButton"),
                        "stop button remained enabled after speed cancellation");
                    AssertElementTextContains(mainWindow, "StatusText", "测速已停止");
                    if (File.Exists(options.OutputPath)
                        || File.Exists(Path.Combine(options.SandboxPath,
                            "signals", "speed-completed.json")))
                    {
                        throw new InvalidOperationException(
                            "Canceled speed test unexpectedly committed an output.");
                    }
                    Trace("speed cancellation and control locking passed");

                    WriteControl(options.SandboxPath, "speed-mode.txt", "success");
                    DeleteSignal(options.SandboxPath, "speed-started.json");
                    DeleteSignal(options.SandboxPath, "speed-completed.json");
                    PostKey(guiProcess.MainWindowHandle, VirtualKeyF5);
                    Trace("successful speed test invoked through F5");

                    WaitUntil(delegate
                    {
                        return File.Exists(options.OutputPath)
                            && File.Exists(Path.Combine(
                                options.SandboxPath, "signals", "speed-completed.json"));
                    }, "speed test did not complete");
                    Trace("speed files completed");
                    IntPtr completionDialog = WaitForNativeWindow(guiProcess.Id, "完成");
                    PostKey(completionDialog, (int)VirtualKeyShort.RETURN);
                    WaitUntil(delegate
                    {
                        return FindNativeWindow(guiProcess.Id, "完成") == IntPtr.Zero;
                    }, "speed completion dialog did not close");
                    mainWindow.SetForeground();
                    Trace("speed completion dialog closed");
                    WaitForEnabled(startButton, "speed test UI did not become idle");
                    Trace("speed UI completed");
                    AssertFileContains(options.OutputPath, "port: 10001", true);
                    AssertFileContains(options.OutputPath, "port: 10002", true);

                    SelectComboBox(mainWindow, "StatusFilter", "失败");
                    AssertComboSelected(mainWindow, "StatusFilter", "失败");
                    AssertVisibleSelectionCopy(mainWindow, resultGrid, ":10003");
                    SelectComboBox(mainWindow, "StatusFilter", "全部");
                    SelectComboBox(mainWindow, "LatencyFilter", "<100ms");
                    AssertComboSelected(mainWindow, "LatencyFilter", "<100ms");
                    AssertVisibleSelectionCopy(mainWindow, resultGrid, ":10001");
                    SelectComboBox(mainWindow, "LatencyFilter", "全部");
                    SelectComboBox(mainWindow, "ProtocolFilter", "Trojan");
                    AssertComboSelected(mainWindow, "ProtocolFilter", "Trojan");
                    AssertVisibleSelectionCopy(mainWindow, resultGrid, ":10002");
                    SelectComboBox(mainWindow, "ProtocolFilter", "全部");
                    Trace("status, latency, and protocol filters use visible-only selection");

                    ClickGridHeaderByIndex(resultGrid, 3);
                    ClickGridHeaderByIndex(resultGrid, 3);
                    ClickGridHeaderByIndex(resultGrid, 3);
                    Trace("list sorting passed");

                    WriteControl(options.SandboxPath, "region-mode.txt", "block-after-one");
                    DeleteSignal(options.SandboxPath, "region-first-event.json");
                    DeleteSignal(options.SandboxPath, "region-completed.json");
                    PostKey(guiProcess.MainWindowHandle, VirtualKeyF6);
                    WaitForSignal(options.SandboxPath, "region-first-event.json");
                    AssertEnabled(mainWindow, "ConfigSourceInput", false);
                    AssertEnabled(mainWindow, "StartSpeedTestButton", false);
                    AssertEnabled(mainWindow, "StopTaskButton", true);
                    PostKey(guiProcess.MainWindowHandle, VirtualKeyEscape);
                    WaitForEnabled(startButton, "Esc did not cancel the region query");
                    WaitForDisabled(FindById(mainWindow, "StopTaskButton"),
                        "stop button remained enabled after region cancellation");
                    AssertElementTextContains(mainWindow, "StatusText", "查询前地区信息已恢复");
                    AssertEnabled(mainWindow, "RegionFilter", false);
                    Trace("region cancellation rollback passed");

                    WriteControl(options.SandboxPath, "region-mode.txt", "all-success");
                    DeleteSignal(options.SandboxPath, "region-first-event.json");
                    DeleteSignal(options.SandboxPath, "region-completed.json");
                    PostKey(guiProcess.MainWindowHandle, VirtualKeyF6);
                    WaitForSignal(options.SandboxPath, "region-completed.json");
                    WaitForElementTextContains(mainWindow, "StatusText", "出口地区查询完成：成功 2",
                        "successful region query was not committed");
                    AssertEnabled(mainWindow, "RegionFilter", true);
                    AssertComboItems(mainWindow, "RegionFilter",
                        new[] { "全部", "HK 香港", "US 美国" });
                    SelectComboBoxByPrefix(mainWindow, "RegionFilter", "HK");
                    AssertComboSelectedByPrefix(mainWindow, "RegionFilter", "HK");
                    AssertVisibleSelectionCopy(mainWindow, resultGrid, ":10001");
                    SelectComboBox(mainWindow, "RegionFilter", "全部");
                    Trace("region filter uses visible-only selection");

                    WriteControl(options.SandboxPath, "region-mode.txt", "malformed");
                    DeleteSignal(options.SandboxPath, "region-first-event.json");
                    DeleteSignal(options.SandboxPath, "region-completed.json");
                    InvokeGridContextCommand(resultGrid, 0, 5);
                    WaitForNativeWindow(guiProcess.Id, "出口地区查询失败");
                    DismissDialog(guiProcess.Id, "出口地区查询失败");
                    WaitForEnabled(startButton, "malformed region query did not return to idle");
                    AssertElementTextContains(mainWindow, "StatusText", "查询前地区信息已恢复");
                    AssertComboItems(mainWindow, "RegionFilter",
                        new[] { "全部", "HK 香港", "US 美国" });
                    Trace("malformed region rollback passed");

                    DeleteSignal(options.SandboxPath, "manage-config-completed.json");
                    Trace("before rename row focus");
                    InvokeGridContextCommand(resultGrid, 0, 3);
                    Trace("rename context command invoked");
                    IntPtr renameDialog = WaitForNativeWindow(
                        guiProcess.Id, "重命名节点");
                    Trace("rename dialog found");
                    SetDialogEditText(
                        automation, renameDialog, "flaui-renamed-a");
                    ClickDialogButton(renameDialog, "保存");
                    Trace("rename saved");
                    WaitUntil(delegate
                    {
                        return FindNativeWindow(guiProcess.Id, "重命名节点") == IntPtr.Zero;
                    }, "rename dialog did not close");
                    Trace("rename dialog closed");
                    WaitForSignal(options.SandboxPath, "manage-config-completed.json");
                    Trace("rename signal received");
                    WaitUntil(delegate
                    {
                        return File.ReadAllText(options.OutputPath).Contains("flaui-renamed-a");
                    }, "renamed node was not committed");
                    DismissDialog(guiProcess.Id, "节点管理完成");
                    Trace("rename completion dialog closed");
                    WaitForEnabled(startButton, "rename UI did not become idle");

                    DeleteSignal(options.SandboxPath, "manage-config-completed.json");
                    Trace("before delete row focus");
                    InvokeGridContextCommand(resultGrid, 1, 4);
                    Trace("delete context command invoked");
                    WaitForNativeWindow(guiProcess.Id, "确认删除");
                    Trace("delete confirmation found");
                    Keyboard.Press(VirtualKeyShort.RETURN);
                    Trace("delete confirmed");
                    WaitForSignal(options.SandboxPath, "manage-config-completed.json");
                    Trace("delete signal received");
                    WaitUntil(delegate
                    {
                        return !File.ReadAllText(options.OutputPath).Contains("port: 10002");
                    }, "deleted node is still present in output");
                    DismissDialog(guiProcess.Id, "节点管理完成");
                    Trace("delete completion dialog closed");
                    WaitForEnabled(startButton, "delete UI did not become idle");

                    mainWindow.SetForeground();
                    string captureMethod = CaptureWindow(
                        guiProcess.MainWindowHandle, options.ScreenshotPath);
                    Trace("screenshot captured: " + captureMethod);
                    ValidateScreenshot(options.ScreenshotPath);

                    Console.WriteLine("FlaUI WinForms flow passed.");
                    Console.WriteLine("Capture method: " + captureMethod);
                    Console.WriteLine("Screenshot: " + options.ScreenshotPath);
                }
                return 0;
            }
            catch (Exception ex)
            {
                Trace("failure: " + ex.GetType().FullName + " " + ex.Message);
                try
                {
                    IntPtr foreground = GetForegroundWindow();
                    if (foreground != IntPtr.Zero && !string.IsNullOrEmpty(tracePath))
                    {
                        CaptureWindow(foreground, Path.Combine(
                            Path.GetDirectoryName(tracePath), "failure-window.png"));
                    }
                }
                catch { }
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
                        if (!guiProcess.WaitForExit(2000)) guiProcess.Kill();
                    }
                    catch { }
                    guiProcess.Dispose();
                }
                if (clipboardCaptured) RestoreClipboardSnapshot(clipboardSnapshot);
            }
        }

        private static void AssertVisibleSelectionCopy(
            AutomationElement root, AutomationElement grid, string expectedIdentity)
        {
            WaitUntil(delegate
            {
                return ElementTextContains(root, "StatisticsText", "筛选后 1");
            }, "list filter did not reduce the grid to one visible row");
            string marker = "ClashSpeedTestGUI-ClipboardTest-" + Guid.NewGuid().ToString("N");
            SetClipboardText(marker);
            grid.Focus();
            WaitUntil(delegate
            {
                return grid.Properties.HasKeyboardFocus.Value;
            }, "result grid did not receive keyboard focus");
            TypeControlKey(VirtualKeyShort.KEY_A);
            InvokeGridContextCommand(grid, 0, 0);
            WaitUntil(delegate
            {
                string value = GetClipboardText();
                return value.Length > 0
                    && !string.Equals(value, marker, StringComparison.Ordinal);
            }, "filtered copy did not update the clipboard");
            string actual = GetClipboardText().Trim();
            string[] lines = actual.Split(new[] { "\r\n", "\n" },
                StringSplitOptions.RemoveEmptyEntries);
            if (lines.Length != 1
                || actual.IndexOf(expectedIdentity, StringComparison.Ordinal) < 0)
                throw new InvalidOperationException(
                    "Filtered copy returned unexpected URLs. Expected one visible node matching "
                    + expectedIdentity + ", actual clipboard: "
                    + actual);
            AssertElementTextContains(root, "StatusText", "已复制 1 个节点 URL");
        }

        private static void TypeControlKey(VirtualKeyShort key)
        {
            Keyboard.Press(VirtualKeyShort.CONTROL);
            try
            {
                Keyboard.Press(key);
                Keyboard.Release(key);
            }
            finally
            {
                Keyboard.Release(VirtualKeyShort.CONTROL);
            }
        }

        private static System.Windows.Forms.DataObject CaptureClipboardSnapshot()
        {
            for (int attempt = 0; attempt < 10; attempt++)
            {
                try
                {
                    System.Windows.Forms.IDataObject source =
                        System.Windows.Forms.Clipboard.GetDataObject();
                    System.Windows.Forms.DataObject snapshot =
                        new System.Windows.Forms.DataObject();
                    if (source == null) return snapshot;
                    foreach (string format in source.GetFormats(false))
                    {
                        object value = source.GetData(format, false);
                        if (value != null) snapshot.SetData(format, false, value);
                    }
                    return snapshot;
                }
                catch (ExternalException)
                {
                    if (attempt == 9) throw;
                    Thread.Sleep(50);
                }
            }
            throw new InvalidOperationException("Could not capture the clipboard.");
        }

        private static void RestoreClipboardSnapshot(System.Windows.Forms.DataObject snapshot)
        {
            for (int attempt = 0; attempt < 10; attempt++)
            {
                try
                {
                    if (snapshot == null || snapshot.GetFormats(false).Length == 0)
                        System.Windows.Forms.Clipboard.Clear();
                    else
                        System.Windows.Forms.Clipboard.SetDataObject(snapshot, true);
                    return;
                }
                catch (ExternalException)
                {
                    if (attempt == 9) throw;
                    Thread.Sleep(50);
                }
            }
        }

        private static void SetClipboardText(string value)
        {
            for (int attempt = 0; attempt < 10; attempt++)
            {
                try
                {
                    System.Windows.Forms.Clipboard.SetText(value);
                    return;
                }
                catch (ExternalException)
                {
                    if (attempt == 9) throw;
                    Thread.Sleep(50);
                }
            }
        }

        private static string GetClipboardText()
        {
            for (int attempt = 0; attempt < 10; attempt++)
            {
                try
                {
                    return System.Windows.Forms.Clipboard.ContainsText()
                        ? System.Windows.Forms.Clipboard.GetText()
                        : "";
                }
                catch (ExternalException)
                {
                    if (attempt == 9) throw;
                    Thread.Sleep(50);
                }
            }
            return "";
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
                    throw new InvalidOperationException("UI fixture launcher did not start.");
                if (!launcher.WaitForExit(5000))
                    throw new TimeoutException("UI fixture launcher did not exit.");
                if (launcher.ExitCode != 0)
                    throw new InvalidOperationException(
                        "UI fixture launcher failed with exit code " + launcher.ExitCode + ".");
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
            }, "fixture GUI process did not expose a window");
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

        private static IntPtr WaitForNativeWindow(int processId, string title)
        {
            IntPtr result = IntPtr.Zero;
            WaitUntil(delegate
            {
                result = FindNativeWindow(processId, title);
                return result != IntPtr.Zero;
            }, "window was not found: " + title);
            return result;
        }

        private static void DismissDialog(int processId, string title)
        {
            IntPtr dialog = WaitForNativeWindow(processId, title);
            PostKey(dialog, (int)VirtualKeyShort.RETURN);
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

        private static void AssertEnabled(
            AutomationElement root, string automationId, bool expected)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null)
                throw new InvalidOperationException(
                    "Control was not found: " + automationId);
            if (element.IsEnabled != expected)
                throw new InvalidOperationException(
                    automationId + " enabled state was " + element.IsEnabled
                    + ", expected " + expected + ".");
        }

        private static void AssertElementTextContains(
            AutomationElement root, string automationId, string expected)
        {
            if (!ElementTextContains(root, automationId, expected))
                throw new InvalidOperationException(
                    automationId + " did not contain expected text '" + expected + "'.");
        }

        private static void WaitForElementTextContains(
            AutomationElement root, string automationId, string expected, string failure)
        {
            WaitUntil(delegate
            {
                return ElementTextContains(root, automationId, expected);
            }, failure);
        }

        private static bool ElementTextContains(
            AutomationElement root, string automationId, string expected)
        {
            AutomationElement element = FindById(root, automationId);
            if (element != null && (element.Name ?? "")
                .IndexOf(expected, StringComparison.Ordinal) >= 0)
                return true;

            AutomationElement statusStrip = FindById(root, "StatusStrip");
            if (statusStrip == null) return false;
            if ((statusStrip.Name ?? "").IndexOf(expected, StringComparison.Ordinal) >= 0)
                return true;
            foreach (AutomationElement child in statusStrip.FindAllDescendants())
            {
                if ((child.Name ?? "").IndexOf(expected, StringComparison.Ordinal) >= 0)
                    return true;
            }
            return false;
        }

        private static void SelectComboBox(
            AutomationElement root, string automationId, string value)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null)
                throw new InvalidOperationException(
                    "Combo box was not found: " + automationId);
            AutomationElement[] items = element.AsComboBox().Items;
            int selectedIndex = -1;
            for (int index = 0; index < items.Length; index++)
            {
                if (string.Equals(items[index].Name, value, StringComparison.Ordinal))
                {
                    selectedIndex = index;
                    break;
                }
            }
            if (selectedIndex < 0)
                throw new InvalidOperationException(
                    automationId + " did not contain item '" + value + "'.");
            SelectComboBoxIndex(element, selectedIndex);
        }

        private static void SelectComboBoxByPrefix(
            AutomationElement root, string automationId, string prefix)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null)
                throw new InvalidOperationException(
                    "Combo box was not found: " + automationId);
            ComboBox combo = element.AsComboBox();
            AutomationElement[] items = combo.Items;
            int selectedIndex = -1;
            for (int index = 0; index < items.Length; index++)
            {
                if ((items[index].Name ?? "").StartsWith(prefix, StringComparison.Ordinal))
                {
                    selectedIndex = index;
                    break;
                }
            }
            if (selectedIndex < 0)
                throw new InvalidOperationException(
                    automationId + " did not contain an item starting with " + prefix + ".");
            SelectComboBoxIndex(element, selectedIndex);
        }

        private static void SelectComboBoxIndex(AutomationElement element, int selectedIndex)
        {
            element.Focus();
            WaitUntil(delegate
            {
                return element.Properties.HasKeyboardFocus.Value;
            }, "combo box did not receive keyboard focus");
            Keyboard.Type(new[] { (VirtualKeyShort)0x24 });
            for (int index = 0; index < selectedIndex; index++)
                Keyboard.Type(new[] { (VirtualKeyShort)0x28 });
        }

        private static void AssertComboItems(
            AutomationElement root, string automationId, string[] expected)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null)
                throw new InvalidOperationException(
                    "Combo box was not found: " + automationId);
            AutomationElement[] items = element.AsComboBox().Items;
            if (items.Length != expected.Length)
                throw new InvalidOperationException(
                    automationId + " contained " + items.Length
                    + " items, expected " + expected.Length + ".");
            for (int index = 0; index < expected.Length; index++)
            {
                if (!string.Equals(items[index].Name, expected[index], StringComparison.Ordinal))
                    throw new InvalidOperationException(
                        automationId + " item " + index + " was '" + items[index].Name
                        + "', expected '" + expected[index] + "'.");
            }
        }

        private static void ClickGridHeaderByIndex(AutomationElement grid, int columnIndex)
        {
            int[] weights = { 45, 220, 100, 100, 100, 100, 130, 100 };
            if (columnIndex < 0 || columnIndex >= weights.Length)
                throw new ArgumentOutOfRangeException("columnIndex");
            Rectangle bounds = grid.BoundingRectangle;
            if (bounds.Width <= 0 || bounds.Height <= 0)
                throw new InvalidOperationException("Result grid bounds are invalid.");
            int totalWeight = 0;
            int precedingWeight = 0;
            for (int index = 0; index < weights.Length; index++)
            {
                totalWeight += weights[index];
                if (index < columnIndex) precedingWeight += weights[index];
            }
            int usableWidth = Math.Max(1, bounds.Width - 20);
            int x = bounds.Left + (usableWidth * precedingWeight / totalWeight)
                + (usableWidth * weights[columnIndex] / totalWeight / 2);
            int y = bounds.Top + Math.Max(8, Math.Min(14, bounds.Height / 20));
            grid.Focus();
            Mouse.LeftClick(new Point(x, y));
            Thread.Sleep(150);
        }

        private static void AssertComboSelected(
            AutomationElement root, string automationId, string expected)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null)
                throw new InvalidOperationException(
                    "Combo box was not found: " + automationId);
            AutomationElement selected = element.AsComboBox().SelectedItem;
            string actual = selected == null ? "" : selected.Name ?? "";
            if (!string.Equals(actual.Trim(), expected, StringComparison.Ordinal))
                throw new InvalidOperationException(
                    automationId + " selected '" + actual.Trim()
                    + "', expected '" + expected + "'.");
        }

        private static void AssertComboSelectedByPrefix(
            AutomationElement root, string automationId, string expectedPrefix)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null)
                throw new InvalidOperationException(
                    "Combo box was not found: " + automationId);
            AutomationElement selected = element.AsComboBox().SelectedItem;
            string actual = selected == null ? "" : selected.Name ?? "";
            if (!actual.StartsWith(expectedPrefix, StringComparison.Ordinal))
                throw new InvalidOperationException(
                    automationId + " selected '" + actual
                    + "', expected prefix '" + expectedPrefix + "'.");
        }

        private static void SetText(AutomationElement root, string automationId, string value)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null) throw new InvalidOperationException(
                "Text input was not found: " + automationId);
            element.AsTextBox().Text = value;
        }

        private static void InvokeButton(AutomationElement root, string automationId)
        {
            AutomationElement element = FindById(root, automationId);
            if (element == null) throw new InvalidOperationException(
                "Button was not found: " + automationId);
            element.AsButton().Invoke();
        }

        private static void InvokeGridContextCommand(
            AutomationElement grid, int offsetFromFirst, int commandIndex)
        {
            Rectangle bounds = grid.BoundingRectangle;
            if (bounds.Width <= 0 || bounds.Height <= 0)
                throw new InvalidOperationException("Result grid bounds are invalid.");
            double scale = GetDpiForWindow(GetForegroundWindow()) / 96D;
            if (scale < 1D || scale > 4D) scale = 1D;
            int headerHeight = Math.Max(23, (int)Math.Round(23D * scale));
            int rowHeight = Math.Max(22, (int)Math.Round(22D * scale));
            Point point = new Point(
                bounds.Left + Math.Max(20, (int)Math.Round(20D * scale)),
                bounds.Top + headerHeight + rowHeight * offsetFromFirst + rowHeight / 2);
            Trace("grid click bounds=" + bounds.ToString()
                + " scale=" + scale.ToString("0.###")
                + " point=" + point.ToString());
            grid.Focus();
            Thread.Sleep(100);
            IntPtr mainHandle = GetForegroundWindow();
            uint processId;
            GetWindowThreadProcessId(mainHandle, out processId);
            Mouse.RightClick(point);
            Thread.Sleep(250);
            IntPtr menuHandle = IntPtr.Zero;
            WaitUntil(delegate
            {
                menuHandle = FindProcessPopup(processId, mainHandle);
                return menuHandle != IntPtr.Zero;
            }, "context menu window was not found");
            Trace("context menu handle=" + menuHandle.ToString());
            if (commandIndex == 5)
            {
                PostKey(menuHandle, VirtualKeyEnd);
                PostKey(menuHandle, (int)VirtualKeyShort.RETURN);
                return;
            }
            NativeRect menuRect;
            if (!GetWindowRect(menuHandle, out menuRect))
                throw new InvalidOperationException("Could not read context menu bounds.");
            int menuWidth = menuRect.Right - menuRect.Left;
            int menuHeight = menuRect.Bottom - menuRect.Top;
            Trace("context menu bounds=" + menuRect.Left + "," + menuRect.Top
                + " " + menuWidth + "x" + menuHeight);
            int menuItemHeight = Math.Max(21, (int)Math.Round(21D * scale));
            int separatorHeight = Math.Max(10, (int)Math.Round(11D * scale));
            int menuBorder = Math.Max(1, (int)Math.Round(2D * scale));
            int itemTop = menuBorder;
            if (commandIndex < 3)
                itemTop += commandIndex * menuItemHeight;
            else if (commandIndex < 5)
                itemTop += 3 * menuItemHeight + separatorHeight
                    + (commandIndex - 3) * menuItemHeight;
            else
                itemTop += 5 * menuItemHeight + 2 * separatorHeight
                    + (commandIndex - 5) * menuItemHeight;
            Point commandPoint = new Point(
                menuRect.Left + menuWidth / 2,
                menuRect.Top + itemTop + menuItemHeight / 2);
            Trace("context command point=" + commandPoint.ToString());
            Mouse.LeftClick(commandPoint);
        }

        private static IntPtr FindProcessPopup(uint processId, IntPtr excludedHandle)
        {
            IntPtr result = IntPtr.Zero;
            int bestArea = int.MaxValue;
            EnumWindows(delegate(IntPtr handle, IntPtr parameter)
            {
                if (handle == excludedHandle || !IsWindowVisible(handle)) return true;
                uint ownerProcess;
                GetWindowThreadProcessId(handle, out ownerProcess);
                if (ownerProcess != processId) return true;
                NativeRect rect;
                if (!GetWindowRect(handle, out rect)) return true;
                int width = rect.Right - rect.Left;
                int height = rect.Bottom - rect.Top;
                int area = width * height;
                if (width < 80 || height < 40 || area >= bestArea) return true;
                result = handle;
                bestArea = area;
                return true;
            }, IntPtr.Zero);
            return result;
        }

        private static void DeleteSignal(string sandboxPath, string fileName)
        {
            string path = Path.Combine(sandboxPath, "signals", fileName);
            if (File.Exists(path)) File.Delete(path);
        }

        private static void WriteControl(
            string sandboxPath, string fileName, string value)
        {
            File.WriteAllText(Path.Combine(sandboxPath, "control", fileName),
                value, new System.Text.UTF8Encoding(false));
        }

        private static void WaitForSignal(string sandboxPath, string fileName)
        {
            string path = Path.Combine(sandboxPath, "signals", fileName);
            WaitUntil(delegate { return File.Exists(path); }, "signal was not written: " + fileName);
        }

        private static void AssertFileContains(string path, string value, bool expected)
        {
            bool actual = File.ReadAllText(path).Contains(value);
            if (actual != expected)
                throw new InvalidOperationException(
                    "Unexpected file content for " + value + " in " + path + ".");
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
                    lastError = null;
                }
                catch (Exception ex)
                {
                    lastError = ex;
                }
                Thread.Sleep(100);
            }
            throw new TimeoutException(failure, lastError);
        }

        private static void WaitForEnabled(AutomationElement element, string failure)
        {
            WaitUntil(delegate { return element.IsAvailable && element.IsEnabled; }, failure);
        }

        private static void WaitForDisabled(AutomationElement element, string failure)
        {
            if (element == null)
                throw new InvalidOperationException("Control was not found before disabled wait.");
            WaitUntil(delegate { return element.IsAvailable && !element.IsEnabled; }, failure);
        }

        private static void Trace(string message)
        {
            if (string.IsNullOrEmpty(tracePath)) return;
            File.AppendAllText(tracePath,
                DateTime.UtcNow.ToString("o") + " " + message + Environment.NewLine);
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

        private static void SetDialogEditText(
            UIA3Automation automation, IntPtr dialogHandle, string value)
        {
            IntPtr child = IntPtr.Zero;
            while (true)
            {
                child = FindWindowEx(dialogHandle, child, null, null);
                if (child == IntPtr.Zero) break;
                System.Text.StringBuilder className = new System.Text.StringBuilder(256);
                GetClassName(child, className, className.Capacity);
                if (className.ToString().IndexOf("EDIT", StringComparison.OrdinalIgnoreCase) < 0)
                    continue;
                AutomationElement edit = automation.FromHandle(child);
                edit.AsTextBox().Text = value;
                Trace("rename text set on class=" + className.ToString());
                return;
            }
            throw new InvalidOperationException("Rename text box was not found.");
        }

        private static void ClickDialogButton(IntPtr dialogHandle, string text)
        {
            IntPtr child = IntPtr.Zero;
            while (true)
            {
                child = FindWindowEx(dialogHandle, child, null, null);
                if (child == IntPtr.Zero) break;
                System.Text.StringBuilder className = new System.Text.StringBuilder(256);
                GetClassName(child, className, className.Capacity);
                if (className.ToString().IndexOf("BUTTON", StringComparison.OrdinalIgnoreCase) < 0)
                    continue;
                int length = GetWindowTextLength(child);
                System.Text.StringBuilder caption = new System.Text.StringBuilder(length + 1);
                GetWindowText(child, caption, caption.Capacity);
                if (!string.Equals(caption.ToString(), text, StringComparison.Ordinal)) continue;
                SendMessage(child, ButtonMessageClick, IntPtr.Zero, IntPtr.Zero);
                Trace("dialog button clicked: " + text);
                return;
            }
            throw new InvalidOperationException("Dialog button was not found: " + text);
        }

        private static string CaptureWindow(IntPtr windowHandle, string outputPath)
        {
            if (windowHandle == IntPtr.Zero)
                throw new InvalidOperationException("GUI window handle is empty.");
            NativeRect rect;
            if (!GetWindowRect(windowHandle, out rect))
                throw new InvalidOperationException("GetWindowRect failed.");
            int width = rect.Right - rect.Left;
            int height = rect.Bottom - rect.Top;
            if (width <= 0 || height <= 0)
                throw new InvalidOperationException("GUI window bounds are invalid.");

            Bitmap bitmap = CaptureWithPrintWindow(windowHandle, width, height);
            string method = "PrintWindow(PW_RENDERFULLCONTENT)";
            if (bitmap == null || IsUniform(bitmap))
            {
                if (bitmap != null) bitmap.Dispose();
                bitmap = CaptureWithBitBlt(rect, width, height);
                method = "BitBlt(screen region)";
            }
            if (bitmap == null || IsUniform(bitmap))
            {
                if (bitmap != null) bitmap.Dispose();
                throw new InvalidOperationException("Both screenshot backends returned no useful image.");
            }

            string directory = Path.GetDirectoryName(outputPath);
            Directory.CreateDirectory(directory);
            using (bitmap)
            {
                bitmap.Save(outputPath, ImageFormat.Png);
            }
            return method;
        }

        private static Bitmap CaptureWithPrintWindow(IntPtr windowHandle, int width, int height)
        {
            Bitmap bitmap = new Bitmap(width, height, PixelFormat.Format32bppArgb);
            using (Graphics graphics = Graphics.FromImage(bitmap))
            {
                IntPtr destination = graphics.GetHdc();
                bool success;
                try { success = PrintWindow(windowHandle, destination, PrintWindowRenderFullContent); }
                finally { graphics.ReleaseHdc(destination); }
                if (!success)
                {
                    bitmap.Dispose();
                    return null;
                }
            }
            return bitmap;
        }

        private static Bitmap CaptureWithBitBlt(NativeRect rect, int width, int height)
        {
            Bitmap bitmap = new Bitmap(width, height, PixelFormat.Format32bppArgb);
            IntPtr source = GetDC(IntPtr.Zero);
            if (source == IntPtr.Zero)
            {
                bitmap.Dispose();
                return null;
            }
            try
            {
                using (Graphics graphics = Graphics.FromImage(bitmap))
                {
                    IntPtr destination = graphics.GetHdc();
                    bool success;
                    try
                    {
                        success = BitBlt(destination, 0, 0, width, height, source,
                            rect.Left, rect.Top, SourceCopy | CaptureBlt);
                    }
                    finally { graphics.ReleaseHdc(destination); }
                    if (!success)
                    {
                        bitmap.Dispose();
                        return null;
                    }
                }
            }
            finally { ReleaseDC(IntPtr.Zero, source); }
            return bitmap;
        }

        private static bool IsUniform(Bitmap bitmap)
        {
            int stepX = Math.Max(1, bitmap.Width / 32);
            int stepY = Math.Max(1, bitmap.Height / 24);
            int minimum = 255;
            int maximum = 0;
            for (int y = 0; y < bitmap.Height; y += stepY)
            {
                for (int x = 0; x < bitmap.Width; x += stepX)
                {
                    Color color = bitmap.GetPixel(x, y);
                    int luminance = (color.R * 30 + color.G * 59 + color.B * 11) / 100;
                    minimum = Math.Min(minimum, luminance);
                    maximum = Math.Max(maximum, luminance);
                }
            }
            return maximum - minimum < 12;
        }

        private static void ValidateScreenshot(string path)
        {
            FileInfo file = new FileInfo(path);
            if (!file.Exists || file.Length < 4096)
                throw new InvalidOperationException("Screenshot file is missing or too small.");
            using (Image image = Image.FromFile(path))
            {
                if (image.Width < 400 || image.Height < 300)
                    throw new InvalidOperationException("Screenshot dimensions are unexpectedly small.");
            }
        }

        [DllImport("user32.dll")]
        private static extern bool GetWindowRect(IntPtr hWnd, out NativeRect rect);

        [DllImport("user32.dll")]
        private static extern IntPtr GetForegroundWindow();

        [DllImport("user32.dll")]
        private static extern uint GetDpiForWindow(IntPtr hWnd);

        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        private static extern IntPtr FindWindow(string className, string windowName);

        private delegate bool EnumWindowsCallback(IntPtr hWnd, IntPtr parameter);

        [DllImport("user32.dll")]
        private static extern bool EnumWindows(EnumWindowsCallback callback, IntPtr parameter);

        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        private static extern IntPtr FindWindowEx(
            IntPtr parent, IntPtr childAfter, string className, string windowName);

        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        private static extern int GetClassName(
            IntPtr hWnd, System.Text.StringBuilder className, int maximumCount);

        [DllImport("user32.dll")]
        private static extern int GetWindowTextLength(IntPtr hWnd);

        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        private static extern int GetWindowText(
            IntPtr hWnd, System.Text.StringBuilder value, int maximumCount);

        [DllImport("user32.dll")]
        private static extern bool IsWindowVisible(IntPtr hWnd);

        [DllImport("user32.dll")]
        private static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);

        [DllImport("user32.dll")]
        private static extern bool PostMessage(
            IntPtr hWnd, int message, IntPtr wParam, IntPtr lParam);

        [DllImport("user32.dll")]
        private static extern IntPtr SendMessage(
            IntPtr hWnd, int message, IntPtr wParam, IntPtr lParam);

        [DllImport("user32.dll")]
        private static extern bool PrintWindow(IntPtr hWnd, IntPtr hdcBlt, uint flags);

        [DllImport("user32.dll")]
        private static extern IntPtr GetDC(IntPtr hWnd);

        [DllImport("user32.dll")]
        private static extern int ReleaseDC(IntPtr hWnd, IntPtr hdc);

        [DllImport("gdi32.dll")]
        private static extern bool BitBlt(IntPtr destination, int x, int y, int width, int height,
            IntPtr source, int sourceX, int sourceY, int rasterOperation);

        [StructLayout(LayoutKind.Sequential)]
        private struct NativeRect
        {
            public int Left;
            public int Top;
            public int Right;
            public int Bottom;
        }

        private sealed class DriverOptions
        {
            public string SandboxPath;
            public string LauncherPath;
            public string InputPath;
            public string OutputPath;
            public string ScreenshotPath;

            public static DriverOptions Parse(string[] args)
            {
                if (args == null || args.Length != 10)
                    throw new ArgumentException(
                        "Expected --sandbox, --launcher, --input, --output and --screenshot.");
                DriverOptions options = new DriverOptions();
                for (int index = 0; index < args.Length; index += 2)
                {
                    string value = Path.GetFullPath(args[index + 1]);
                    switch (args[index])
                    {
                        case "--sandbox": options.SandboxPath = value; break;
                        case "--launcher": options.LauncherPath = value; break;
                        case "--input": options.InputPath = value; break;
                        case "--output": options.OutputPath = value; break;
                        case "--screenshot": options.ScreenshotPath = value; break;
                        default: throw new ArgumentException("Unknown option: " + args[index]);
                    }
                }
                if (string.IsNullOrEmpty(options.SandboxPath)
                    || string.IsNullOrEmpty(options.LauncherPath)
                    || string.IsNullOrEmpty(options.InputPath)
                    || string.IsNullOrEmpty(options.OutputPath)
                    || string.IsNullOrEmpty(options.ScreenshotPath))
                    throw new ArgumentException("One or more required options are missing.");
                return options;
            }
        }
    }
}

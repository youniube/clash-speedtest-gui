using System;
using System.Collections;
using System.Collections.Generic;
using System.ComponentModel;
using System.Diagnostics;
using System.Drawing;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using System.Web.Script.Serialization;
using System.Windows.Forms;

[assembly: AssemblyVersion("2.1.0.0")]
[assembly: AssemblyFileVersion("2.1.0.0")]
[assembly: AssemblyInformationalVersion("2.1.0")]

namespace ClashSpeedTestGUI
{
    internal static class Program
    {
        [STAThread]
        private static void Main(string[] args)
        {
            if (args.Length == 3
                && string.Equals(args[0], "--self-test-region-child", StringComparison.Ordinal))
            {
                using (EventWaitHandle ready = EventWaitHandle.OpenExisting(args[2]))
                {
                    Console.WriteLine("@protocol\t2");
                    Console.WriteLine("@regions\t1");
                    if (string.Equals(args[1], "malformed-block", StringComparison.Ordinal))
                        Console.WriteLine("@regionjson\t!!!");
                    Console.Out.Flush();
                    ready.Set();
                    if (string.Equals(args[1], "malformed-block", StringComparison.Ordinal)
                        || string.Equals(args[1], "missing-block", StringComparison.Ordinal))
                        Thread.Sleep(Timeout.Infinite);
                    if (string.Equals(args[1], "partial-nonzero", StringComparison.Ordinal))
                        Environment.ExitCode = 7;
                }
                return;
            }
            if (args.Length == 2
                && string.Equals(args[0], "--self-test-block-child", StringComparison.Ordinal))
            {
                using (EventWaitHandle ready = EventWaitHandle.OpenExisting(args[1]))
                {
                    ready.Set();
                    Thread.Sleep(Timeout.Infinite);
                }
                return;
            }
            if (args.Length > 0 && string.Equals(args[0], "--self-test", StringComparison.OrdinalIgnoreCase))
            {
                Environment.ExitCode = SelfTests.Run() ? 0 : 1;
                return;
            }

            ServicePointManager.SecurityProtocol = SecurityProtocolType.Tls12;
            Application.EnableVisualStyles();
            Application.SetCompatibleTextRenderingDefault(false);
            Application.Run(new MainForm());
        }
    }

    internal sealed class AppSettings
    {
        public string ConfigSource { get; set; }
        public string FilterRegex { get; set; }
        public decimal MaxLatencyMs { get; set; }
        public decimal MinDownloadSpeed { get; set; }
        public string OutputPath { get; set; }
        public bool RenameNodes { get; set; }
        public bool GistEnabled { get; set; }
        public string GistUsername { get; set; }
        // Retained only to migrate settings written by older versions.
        public string GistAddress { get; set; }
        public string GistToken { get; set; }
        public bool AdvancedExpanded { get; set; }
        public string SpeedMode { get; set; }
        public string BlockKeywords { get; set; }
        public string ServerUrl { get; set; }
        public decimal DownloadSizeMb { get; set; }
        public decimal ProbeTimeoutSeconds { get; set; }
        public decimal TimeoutSeconds { get; set; }
        public decimal Concurrent { get; set; }
        public decimal TransferConcurrent { get; set; }
        public decimal MaxHTTPProbeFailure { get; set; }
        public string UserAgent { get; set; }

        public static AppSettings CreateDefault()
        {
            return new AppSettings
            {
                ConfigSource = "",
                FilterRegex = "",
                MaxLatencyMs = 800,
                MinDownloadSpeed = 3,
                OutputPath = "filtered.yaml",
                RenameNodes = false,
                GistEnabled = false,
                GistUsername = "",
                GistAddress = "",
                GistToken = "",
                AdvancedExpanded = false,
                SpeedMode = "download",
                BlockKeywords = "",
                ServerUrl = "https://dl.google.com/chrome/mac/universal/stable/GGRO/googlechrome.dmg",
                DownloadSizeMb = 20,
                ProbeTimeoutSeconds = 3,
                TimeoutSeconds = 8,
                Concurrent = 4,
                TransferConcurrent = 4,
                MaxHTTPProbeFailure = 50,
                UserAgent = ""
            };
        }
    }

    internal static class SettingsStore
    {
        public const string SettingsDirectoryEnvironmentVariable =
            "CLASH_SPEEDTEST_GUI_SETTINGS_DIRECTORY";

        private static string SettingsDirectory
        {
            get
            {
                string overridePath = Environment.GetEnvironmentVariable(
                    SettingsDirectoryEnvironmentVariable);
                if (!string.IsNullOrWhiteSpace(overridePath) && Path.IsPathRooted(overridePath))
                    return Path.GetFullPath(overridePath);
                return Path.Combine(Environment.GetFolderPath(
                    Environment.SpecialFolder.ApplicationData), "ClashSpeedTestGUI");
            }
        }

        public static string SettingsPath
        {
            get { return Path.Combine(SettingsDirectory, "settings.json"); }
        }

        public static AppSettings Load()
        {
            AppSettings defaults = AppSettings.CreateDefault();
            try
            {
                if (!File.Exists(SettingsPath))
                {
                    return defaults;
                }

                JavaScriptSerializer serializer = new JavaScriptSerializer();
                string json = File.ReadAllText(SettingsPath, Encoding.UTF8);
                Dictionary<string, object> raw = serializer.DeserializeObject(json)
                    as Dictionary<string, object>;
                bool rewriteLegacy = raw != null && (raw.ContainsKey("NodeNotes")
                    || raw.ContainsKey("UploadSizeMb") || raw.ContainsKey("MinUploadSpeed")
                    || raw.ContainsKey("MaxPacketLoss"));
                AppSettings loaded = serializer.Deserialize<AppSettings>(json);
                if (loaded == null)
                {
                    return defaults;
                }

                if (raw != null && raw.ContainsKey("MaxPacketLoss"))
                {
                    try
                    {
                        loaded.MaxHTTPProbeFailure = Convert.ToDecimal(
                            raw["MaxPacketLoss"], CultureInfo.InvariantCulture);
                    }
                    catch { }
                }
                else if (raw == null || !raw.ContainsKey("MaxHTTPProbeFailure"))
                {
                    loaded.MaxHTTPProbeFailure = defaults.MaxHTTPProbeFailure;
                }
                if (string.Equals(loaded.SpeedMode, "full", StringComparison.OrdinalIgnoreCase))
                {
                    loaded.SpeedMode = "download";
                    rewriteLegacy = true;
                }
                Normalize(loaded, defaults);
                if (rewriteLegacy)
                {
                    try { Save(loaded); } catch { }
                }
                return loaded;
            }
            catch
            {
                return defaults;
            }
        }

        public static void Save(AppSettings settings)
        {
            Directory.CreateDirectory(SettingsDirectory);
            JavaScriptSerializer serializer = new JavaScriptSerializer();
            string json = serializer.Serialize(settings);
            string temporaryPath = SettingsPath + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try
            {
                File.WriteAllText(temporaryPath, json, new UTF8Encoding(false));
                AtomicFile.Commit(temporaryPath, SettingsPath);
            }
            finally
            {
                if (File.Exists(temporaryPath)) File.Delete(temporaryPath);
            }
        }

        private static void Normalize(AppSettings value, AppSettings defaults)
        {
            if (value.ConfigSource == null) value.ConfigSource = defaults.ConfigSource;
            if (string.IsNullOrWhiteSpace(value.FilterRegex)
                || string.Equals(value.FilterRegex.Trim(), ".+", StringComparison.Ordinal))
            {
                value.FilterRegex = defaults.FilterRegex;
            }
            if (value.OutputPath == null) value.OutputPath = defaults.OutputPath;
            if (value.GistUsername == null) value.GistUsername = defaults.GistUsername;
            if (value.GistAddress == null) value.GistAddress = defaults.GistAddress;
            if (value.GistToken == null) value.GistToken = defaults.GistToken;
            if (string.IsNullOrWhiteSpace(value.GistUsername)
                && !string.IsNullOrWhiteSpace(value.GistAddress))
            {
                value.GistUsername = GistClient.TryExtractUsername(value.GistAddress);
            }
            if (string.IsNullOrWhiteSpace(value.SpeedMode)) value.SpeedMode = defaults.SpeedMode;
            if (!string.Equals(value.SpeedMode, "fast", StringComparison.OrdinalIgnoreCase)
                && !string.Equals(value.SpeedMode, "download", StringComparison.OrdinalIgnoreCase))
                value.SpeedMode = defaults.SpeedMode;
            if (value.BlockKeywords == null) value.BlockKeywords = defaults.BlockKeywords;
            if (string.IsNullOrWhiteSpace(value.ServerUrl)) value.ServerUrl = defaults.ServerUrl;
            if (value.UserAgent == null) value.UserAgent = defaults.UserAgent;
            if (value.MaxLatencyMs < 0) value.MaxLatencyMs = defaults.MaxLatencyMs;
            if (value.MinDownloadSpeed < 0) value.MinDownloadSpeed = defaults.MinDownloadSpeed;
            if (value.DownloadSizeMb <= 0) value.DownloadSizeMb = defaults.DownloadSizeMb;
            if (value.ProbeTimeoutSeconds <= 0) value.ProbeTimeoutSeconds = defaults.ProbeTimeoutSeconds;
            if (value.TimeoutSeconds <= 0) value.TimeoutSeconds = defaults.TimeoutSeconds;
            if (value.Concurrent <= 0) value.Concurrent = defaults.Concurrent;
            if (value.TransferConcurrent <= 0) value.TransferConcurrent = defaults.TransferConcurrent;
            if (value.MaxHTTPProbeFailure < 0 || value.MaxHTTPProbeFailure > 100)
                value.MaxHTTPProbeFailure = defaults.MaxHTTPProbeFailure;
        }
    }

    internal sealed class RunOptions
    {
        public string ConfigSource;
        public string FilterRegex;
        public double MaxLatencyMs;
        public double MinDownloadSpeed;
        public string OutputPath;
        public string CoreOutputPath;
        public bool RenameNodes;
        public bool GistEnabled;
        public string GistUsername;
        public string GistToken;
        public string SpeedMode;
        public string BlockKeywords;
        public string ServerUrl;
        public int DownloadSizeBytes;
        public double ProbeTimeoutSeconds;
        public double TimeoutSeconds;
        public int NodeConcurrent;
        public int TransferConcurrent;
        public double MaxHTTPProbeFailure;
        public string UserAgent;
    }

    internal sealed class SpeedPreset
    {
        public string Mode;
        public decimal MaxLatencyMs;
        public decimal MinDownloadSpeed;
        public decimal DownloadSizeMb;
        public decimal ProbeTimeoutSeconds;
        public decimal TimeoutSeconds;
        public decimal NodeConcurrent;
        public decimal TransferConcurrent;
        public decimal MaxHTTPProbeFailure;
        public string Hint;
    }

    internal static class SpeedPresets
    {
        public static SpeedPreset Get(int index)
        {
            if (index == 0)
            {
                return new SpeedPreset
                {
                    Mode = "fast",
                    MaxLatencyMs = 1000,
                    MinDownloadSpeed = 0,
                    DownloadSizeMb = 20,
                    ProbeTimeoutSeconds = 2,
                    TimeoutSeconds = 8,
                    NodeConcurrent = 8,
                    TransferConcurrent = 1,
                    MaxHTTPProbeFailure = 100,
                    Hint = "仅测延迟和连通性，几乎不消耗测速流量。"
                };
            }
            if (index == 1)
            {
                return new SpeedPreset
                {
                    Mode = "download",
                    MaxLatencyMs = 800,
                    MinDownloadSpeed = 3,
                    DownloadSizeMb = 20,
                    ProbeTimeoutSeconds = 3,
                    TimeoutSeconds = 8,
                    NodeConcurrent = 4,
                    TransferConcurrent = 4,
                    MaxHTTPProbeFailure = 50,
                    Hint = "测试下载速度，每个节点最多约使用 20MB 流量。"
                };
            }
            if (index == 2)
            {
                return new SpeedPreset
                {
                    Mode = "download",
                    MaxLatencyMs = 500,
                    MinDownloadSpeed = 8,
                    DownloadSizeMb = 50,
                    ProbeTimeoutSeconds = 3,
                    TimeoutSeconds = 10,
                    NodeConcurrent = 3,
                    TransferConcurrent = 4,
                    MaxHTTPProbeFailure = 10,
                    Hint = "大流量纯下载测试，每个节点最多约使用 50MB，筛选阈值更严格。"
                };
            }
            return null;
        }
    }

    internal sealed class NodeSnapshot
    {
        public string Id;
        public string Name;
        public string Type;
        public string ShareUrl;
        public string ShareError;
        public Dictionary<string, object> Config;
        public double LatencyMs;
        public double DownloadMbps;
        public bool DownloadTested;
        public bool DownloadComplete;
        public bool ProbeCompleted;
        public bool DownloadStarted;
        public string State;
        public string StatusDetail;
        public string RegionState;
        public string RegionCountryCode;
        public string RegionCountry;
        public string RegionCity;
        public string RegionEmoji;
        public string RegionError;
        public bool Exported;
    }

    internal sealed class NodeListFilterCriteria
    {
        public string Status;
        public double MaxLatencyExclusive;
        public string Protocol;
        public string RegionCountryCode;
    }

    internal static class NodeListFilter
    {
        public static bool Matches(NodeSnapshot node, NodeListFilterCriteria criteria)
        {
            if (node == null) return false;
            if (criteria == null) return true;

            if (string.Equals(criteria.Status, "有效", StringComparison.Ordinal)
                && !IsValidState(node.State)) return false;
            if (string.Equals(criteria.Status, "失败", StringComparison.Ordinal)
                && !IsFailedState(node.State)) return false;
            if (criteria.MaxLatencyExclusive > 0
                && (node.LatencyMs <= 0 || node.LatencyMs >= criteria.MaxLatencyExclusive)) return false;
            if (!string.IsNullOrWhiteSpace(criteria.Protocol)
                && !string.Equals(NormalizeProtocol(node.Type), NormalizeProtocol(criteria.Protocol),
                    StringComparison.OrdinalIgnoreCase)) return false;
            if (!string.IsNullOrWhiteSpace(criteria.RegionCountryCode)
                && (!string.Equals(node.RegionState, "成功", StringComparison.Ordinal)
                    || !string.Equals(node.RegionCountryCode, criteria.RegionCountryCode,
                        StringComparison.OrdinalIgnoreCase))) return false;
            return true;
        }

        private static bool IsValidState(string value)
        {
            return string.Equals(value, "有效", StringComparison.Ordinal)
                || string.Equals(value, "通过", StringComparison.Ordinal);
        }

        private static bool IsFailedState(string value)
        {
            return string.Equals(value, "失败", StringComparison.Ordinal)
                || string.Equals(value, "未通过", StringComparison.Ordinal);
        }

        private static string NormalizeProtocol(string value)
        {
            string protocol = (value ?? "").Trim().ToLowerInvariant();
            if (protocol == "shadowsocks") return "ss";
            if (protocol == "hy2") return "hysteria2";
            return protocol;
        }
    }

    internal sealed class NodeManifestEvent
    {
        public string id { get; set; }
        public string name { get; set; }
        public string type { get; set; }
        public string share_url { get; set; }
        public string share_error { get; set; }
        public Dictionary<string, object> config { get; set; }
    }

    internal sealed class NodeResultEvent
    {
        public string id { get; set; }
        public string[] cells { get; set; }
        public bool? usable { get; set; }
        public NodeResultMetricsEvent metrics { get; set; }
    }

    internal sealed class NodeResultMetricsEvent
    {
        public long? latency_nanoseconds { get; set; }
        public long? jitter_nanoseconds { get; set; }
        public double? http_probe_failure_percent { get; set; }
        public double? download_bytes_per_second { get; set; }
        public bool? download_tested { get; set; }
        public bool? download_complete { get; set; }
    }

    internal sealed class NodeProgressEvent
    {
        public string id { get; set; }
        public string stage { get; set; }
    }

    internal static class NodeResultProjection
    {
        public static void Apply(NodeSnapshot node, NodeResultEvent result)
        {
            if (node == null || result == null || result.metrics == null
                || !result.usable.HasValue
                || !result.metrics.latency_nanoseconds.HasValue
                || !result.metrics.jitter_nanoseconds.HasValue
                || !result.metrics.http_probe_failure_percent.HasValue
                || !result.metrics.download_bytes_per_second.HasValue
                || !result.metrics.download_tested.HasValue
                || !result.metrics.download_complete.HasValue)
                throw new InvalidOperationException("无法应用不完整的 v5 结果事件。");
            node.LatencyMs = result.metrics.latency_nanoseconds.Value / 1000000D;
            node.DownloadTested = result.metrics.download_tested.Value;
            node.DownloadComplete = result.metrics.download_complete.Value;
            node.DownloadMbps = result.metrics.download_bytes_per_second.Value / 1024D / 1024D;
            node.State = result.usable.Value ? "有效" : "失败";
            node.StatusDetail = "";
            node.RegionState = result.usable.Value ? "未查询" : "不查询";
        }
    }

    internal enum TaskOperationKind
    {
        SpeedTest,
        RegionQuery,
        NodeManagement
    }

    internal sealed class TaskOperation : IDisposable
    {
        private readonly CancellationTokenSource cancellation = new CancellationTokenSource();

        public readonly int Id;
        public readonly TaskOperationKind Kind;
        public bool OutputCommitted;

        public CancellationToken Token { get { return cancellation.Token; } }
        public bool IsCancellationRequested { get { return cancellation.IsCancellationRequested; } }

        public TaskOperation(int id, TaskOperationKind kind)
        {
            Id = id;
            Kind = kind;
        }

        public void Cancel()
        {
            if (!cancellation.IsCancellationRequested) cancellation.Cancel();
        }

        public void Dispose()
        {
            cancellation.Dispose();
        }
    }

    internal static class TaskControlPolicy
    {
        public static bool InputsEnabled(bool busy)
        {
            return !busy;
        }

        public static bool StopEnabled(TaskOperation operation)
        {
            return operation != null
                && !operation.IsCancellationRequested
                && (operation.Kind == TaskOperationKind.SpeedTest
                    || operation.Kind == TaskOperationKind.RegionQuery
                    || operation.Kind == TaskOperationKind.NodeManagement);
        }
    }

    internal static class DpiAwarenessProbe
    {
        [DllImport("shcore.dll")]
        private static extern int GetProcessDpiAwareness(
            IntPtr processHandle, out int awareness);

        public static int Current()
        {
            try
            {
                int awareness;
                return GetProcessDpiAwareness(IntPtr.Zero, out awareness) == 0
                    ? awareness : -1;
            }
            catch
            {
                return -1;
            }
        }
    }

    internal static class OptionsLayoutPolicy
    {
        public static int PanelHeight(
            int clientHeight, int statusHeight, int desiredHeight,
            int minimumPanelHeight, int minimumGridHeight)
        {
            int maximum = Math.Max(minimumPanelHeight,
                clientHeight - statusHeight - minimumGridHeight);
            return Math.Min(desiredHeight, maximum);
        }
    }

    internal sealed class ChildProcessLease : IDisposable
    {
        private readonly Process process;
        private CancellationTokenRegistration registration;
        private bool completed;
        private int disposed;

        public ChildProcessLease(
            Process process, CancellationTokenRegistration registration)
        {
            this.process = process;
            this.registration = registration;
        }

        public void Complete()
        {
            completed = true;
        }

        public void Dispose()
        {
            if (Interlocked.Exchange(ref disposed, 1) != 0) return;
            registration.Dispose();
            if (!completed) ChildProcessLifetime.Terminate(process, 2000);
        }
    }

    internal static class ChildProcessLifetime
    {
        public static ChildProcessLease Start(
            Process process, CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            CancellationTokenRegistration registration = default(CancellationTokenRegistration);
            try
            {
                if (!process.Start())
                    throw new InvalidOperationException("无法启动子进程。");
                registration = cancellationToken.Register(delegate { TryKill(process); });
                if (cancellationToken.IsCancellationRequested) TryKill(process);
                cancellationToken.ThrowIfCancellationRequested();
                return new ChildProcessLease(process, registration);
            }
            catch
            {
                registration.Dispose();
                Terminate(process, 2000);
                throw;
            }
        }

        public static void TryKill(Process process)
        {
            if (process == null) return;
            try
            {
                if (!process.HasExited) process.Kill();
            }
            catch
            {
            }
        }

        public static void Terminate(Process process, int waitMilliseconds)
        {
            TryKill(process);
            if (process == null) return;
            try
            {
                process.WaitForExit(Math.Max(0, waitMilliseconds));
            }
            catch
            {
            }
        }
    }

    internal sealed class RunnerProtocolValidator
    {
        private readonly object sync = new object();
        private readonly List<string> expectedHeaders;
        private readonly Dictionary<string, NodeManifestEvent> nodes =
            new Dictionary<string, NodeManifestEvent>(StringComparer.Ordinal);
        private readonly HashSet<string> results = new HashSet<string>(StringComparer.Ordinal);
        private readonly HashSet<string> probeCompleted = new HashSet<string>(StringComparer.Ordinal);
        private readonly HashSet<string> downloadStarted = new HashSet<string>(StringComparer.Ordinal);
        private readonly HashSet<string> legacyNodeMirrors =
            new HashSet<string>(StringComparer.Ordinal);
        private int legacyResultMirrorCount;
        private bool protocolAccepted;
        private bool headerAccepted;
        private bool nodeCountAccepted;
        private int expectedNodeCount = -1;

        public RunnerProtocolValidator(IEnumerable<string> headers)
        {
            expectedHeaders = new List<string>(headers ?? Enumerable.Empty<string>());
        }

        public bool ProtocolAccepted
        {
            get { lock (sync) return protocolAccepted; }
        }

        public bool IsComplete
        {
            get
            {
                lock (sync)
                {
                    return protocolAccepted && headerAccepted && nodeCountAccepted
                        && expectedNodeCount > 0 && nodes.Count == expectedNodeCount
                        && probeCompleted.Count == expectedNodeCount
                        && results.Count == expectedNodeCount;
                }
            }
        }

        public int ExpectedNodeCount
        {
            get { lock (sync) return expectedNodeCount; }
        }

        public int ResultCount
        {
            get { lock (sync) return results.Count; }
        }

        public static void ValidateResultEnvelope(Dictionary<string, object> value)
        {
            string[] topLevelKeys = { "id", "cells", "usable", "metrics" };
            if (!HasExactKeys(value, topLevelKeys))
                Reject("v5 结果事件顶层字段不完整或包含未知字段。");
            object[] cells = value["cells"] as object[];
            if (!(value["id"] is string) || cells == null
                || cells.Any(delegate(object cell) { return !(cell is string); })
                || !(value["usable"] is bool))
                Reject("v5 结果事件的 id、cells 或 usable 类型错误。");
            Dictionary<string, object> metrics = value["metrics"] as Dictionary<string, object>;
            string[] metricKeys =
            {
                "latency_nanoseconds", "jitter_nanoseconds", "http_probe_failure_percent",
                "download_bytes_per_second", "download_tested", "download_complete"
            };
            if (!HasExactKeys(metrics, metricKeys))
                Reject("v5 结果事件原始指标字段不完整或包含未知字段。");
            if (!IsJsonInteger(metrics["latency_nanoseconds"])
                || !IsJsonInteger(metrics["jitter_nanoseconds"])
                || !IsJsonNumber(metrics["http_probe_failure_percent"])
                || !IsJsonNumber(metrics["download_bytes_per_second"])
                || !(metrics["download_tested"] is bool)
                || !(metrics["download_complete"] is bool))
                Reject("v5 结果事件原始指标字段类型错误。");
        }

        public static void ValidateProgressEnvelope(Dictionary<string, object> value)
        {
            if (!HasExactKeys(value, new[] { "id", "stage" })
                || !(value["id"] is string) || !(value["stage"] is string))
                Reject("v5 进度事件字段不完整、包含未知字段或类型错误。");
        }

        private static bool IsJsonInteger(object value)
        {
            return value is int || value is long;
        }

        private static bool IsJsonNumber(object value)
        {
            return value is int || value is long || value is float
                || value is double || value is decimal;
        }

        private static bool HasExactKeys(
            Dictionary<string, object> value, IEnumerable<string> expectedKeys)
        {
            if (value == null) return false;
            HashSet<string> expected = new HashSet<string>(expectedKeys, StringComparer.Ordinal);
            return value.Count == expected.Count
                && value.Keys.All(delegate(string key) { return expected.Contains(key); });
        }

        public void AcceptProtocol(int version)
        {
            lock (sync)
            {
                if (protocolAccepted || headerAccepted || nodeCountAccepted || nodes.Count > 0
                    || probeCompleted.Count > 0 || downloadStarted.Count > 0 || results.Count > 0)
                    Reject("协议版本行重复或顺序错误。");
                if (version != 5) Reject("仅支持测速事件协议 v5，实际为 v" + version + "。");
                protocolAccepted = true;
            }
        }

        public void AcceptHeader(IEnumerable<string> headers)
        {
            lock (sync)
            {
                if (!protocolAccepted) Reject("表头出现在协议版本之前。");
                if (headerAccepted) Reject("表头重复。");
                List<string> actual = new List<string>(headers ?? Enumerable.Empty<string>());
                if (!actual.SequenceEqual(expectedHeaders))
                    Reject("表头与当前测速模式不匹配。");
                headerAccepted = true;
            }
        }

        public void AcceptNodeCount(int count)
        {
            lock (sync)
            {
                if (!headerAccepted) Reject("节点总数出现在表头之前。");
                if (nodeCountAccepted) Reject("节点总数重复。");
                if (count <= 0) Reject("节点总数必须大于 0。");
                expectedNodeCount = count;
                nodeCountAccepted = true;
            }
        }

        public void AcceptNode(NodeManifestEvent value)
        {
            lock (sync)
            {
                if (!nodeCountAccepted) Reject("节点事件出现在节点总数之前。");
                if (probeCompleted.Count > 0 || downloadStarted.Count > 0 || results.Count > 0)
                    Reject("进度或结果事件开始后又收到节点事件。");
                if (value == null || string.IsNullOrWhiteSpace(value.id)
                    || string.IsNullOrWhiteSpace(value.name) || string.IsNullOrWhiteSpace(value.type)
                    || value.config == null)
                    Reject("节点事件缺少 id、名称、类型或完整配置。");
                object configName;
                object configType;
                if (!value.config.TryGetValue("name", out configName)
                    || !string.Equals(Convert.ToString(configName, CultureInfo.InvariantCulture),
                        value.name, StringComparison.Ordinal)
                    || !value.config.TryGetValue("type", out configType)
                    || string.IsNullOrWhiteSpace(Convert.ToString(configType, CultureInfo.InvariantCulture)))
                    Reject("节点事件配置中的名称或类型无效。");
                if (nodes.ContainsKey(value.id)) Reject("节点 ID 重复：" + value.id);
                if (nodes.Count >= expectedNodeCount) Reject("节点事件数量超过声明总数。");
                nodes[value.id] = value;
            }
        }

        public void AcceptProgress(NodeProgressEvent value)
        {
            lock (sync)
            {
                if (!nodeCountAccepted || nodes.Count != expectedNodeCount)
                    Reject("进度事件在完整节点清单之前到达。");
                if (value == null || string.IsNullOrWhiteSpace(value.id)
                    || string.IsNullOrWhiteSpace(value.stage))
                    Reject("进度事件缺少稳定节点 ID 或阶段。");
                if (!nodes.ContainsKey(value.id)) Reject("进度引用未知节点 ID：" + value.id);
                if (results.Contains(value.id)) Reject("最终结果后又收到进度事件：" + value.id);

                if (string.Equals(value.stage, "probe_completed", StringComparison.Ordinal))
                {
                    if (!probeCompleted.Add(value.id)) Reject("节点 probe_completed 重复：" + value.id);
                    return;
                }
                if (string.Equals(value.stage, "download_started", StringComparison.Ordinal))
                {
                    if (expectedHeaders.IndexOf("下载速度") < 0)
                        Reject("快速模式不能出现 download_started。");
                    if (!probeCompleted.Contains(value.id))
                        Reject("download_started 必须发生在 probe_completed 之后：" + value.id);
                    if (!downloadStarted.Add(value.id)) Reject("节点 download_started 重复：" + value.id);
                    return;
                }
                Reject("未知测速进度阶段：" + value.stage);
            }
        }

        public void AcceptResult(NodeResultEvent value)
        {
            lock (sync)
            {
                if (!nodeCountAccepted || nodes.Count != expectedNodeCount)
                    Reject("结果事件在完整节点清单之前到达。");
                if (value == null || string.IsNullOrWhiteSpace(value.id) || value.cells == null)
                    Reject("结果事件缺少 id 或单元格。");
                NodeManifestEvent node;
                if (!nodes.TryGetValue(value.id, out node)) Reject("结果引用未知节点 ID：" + value.id);
                if (!probeCompleted.Contains(value.id))
                    Reject("最终结果出现在 probe_completed 之前：" + value.id);
                if (results.Contains(value.id)) Reject("节点结果重复：" + value.id);
                if (value.cells.Length != expectedHeaders.Count) Reject("结果列数与表头不匹配。");
                if (value.cells.Any(delegate(string cell) { return cell == null; }))
                    Reject("结果事件包含空单元格。");
                ValidateResultMetrics(value);
                if (value.metrics.download_tested.Value != downloadStarted.Contains(value.id))
                    Reject("下载测试标志与 download_started 进度不一致：" + value.id);
                int nameIndex = expectedHeaders.IndexOf("节点名称");
                int typeIndex = expectedHeaders.IndexOf("类型");
                if (nameIndex < 0 || typeIndex < 0
                    || !string.Equals(value.cells[nameIndex], node.name, StringComparison.Ordinal)
                    || !string.Equals(value.cells[typeIndex], node.type, StringComparison.Ordinal))
                    Reject("结果中的节点名称或类型与清单不一致。");
                results.Add(value.id);
                if (results.Count > expectedNodeCount) Reject("结果事件数量超过声明总数。");
            }
        }

        private void ValidateResultMetrics(NodeResultEvent value)
        {
            NodeResultMetricsEvent metrics = value.metrics;
            if (!value.usable.HasValue || metrics == null
                || !metrics.latency_nanoseconds.HasValue
                || !metrics.jitter_nanoseconds.HasValue
                || !metrics.http_probe_failure_percent.HasValue
                || !metrics.download_bytes_per_second.HasValue
                || !metrics.download_tested.HasValue
                || !metrics.download_complete.HasValue)
                Reject("v5 结果事件缺少 usable 或原始指标字段。");

            double probeFailure = metrics.http_probe_failure_percent.Value;
            double downloadSpeed = metrics.download_bytes_per_second.Value;
            if (metrics.latency_nanoseconds.Value < 0 || metrics.jitter_nanoseconds.Value < 0
                || !IsFinite(probeFailure) || probeFailure < 0 || probeFailure > 100
                || !IsFinite(downloadSpeed) || downloadSpeed < 0)
                Reject("v5 结果事件包含越界或非有限原始指标。");

            bool fast = expectedHeaders.IndexOf("下载速度") < 0;
            if (fast && (metrics.download_tested.Value || metrics.download_complete.Value))
                Reject("快速模式结果不能标记下载已测试或已完成。");
            if (metrics.download_complete.Value && !metrics.download_tested.Value)
                Reject("下载未启动时不能标记为传输完成。");
            if (!metrics.download_tested.Value && downloadSpeed != 0)
                Reject("未执行下载测试却返回了非零下载速度。");
            if (metrics.download_complete.Value && downloadSpeed <= 0)
                Reject("标记下载完成时必须返回正速度。");
            if (value.usable.Value
                && (metrics.latency_nanoseconds.Value <= 0 || probeFailure >= 100))
                Reject("标记为有效的结果缺少可用延迟或 HTTP 探测失败率无效。");
            if (value.usable.Value && !fast
                && (!metrics.download_tested.Value || !metrics.download_complete.Value
                    || downloadSpeed <= 0))
                Reject("标记为有效的结果必须完整执行下载传输。");
        }

        private static bool IsFinite(double value)
        {
            return !double.IsNaN(value) && !double.IsInfinity(value);
        }

        public void AcceptLegacyNodeMirror(string name, string type)
        {
            lock (sync)
            {
                if (!nodeCountAccepted) Reject("兼容节点行出现在节点总数之前。");
                NodeManifestEvent match = nodes.Values.FirstOrDefault(delegate(NodeManifestEvent node)
                {
                    return string.Equals(node.name, name, StringComparison.Ordinal)
                        && string.Equals(node.type, type, StringComparison.Ordinal);
                });
                if (match == null) Reject("兼容节点行与节点事件不一致。");
                if (!legacyNodeMirrors.Add(match.id)) Reject("兼容节点行重复：" + name);
            }
        }

        public void AcceptLegacyResultMirror(string[] cells)
        {
            lock (sync)
            {
                if (cells == null || cells.Length != expectedHeaders.Count)
                    Reject("兼容结果行列数错误。");
                legacyResultMirrorCount++;
                if (legacyResultMirrorCount > results.Count)
                    Reject("兼容结果行出现在对应结果事件之前或发生重复。");
            }
        }

        public void ValidateCompletion()
        {
            lock (sync)
            {
                if (!protocolAccepted) Reject("缺少协议版本。");
                if (!headerAccepted) Reject("缺少结果表头。");
                if (!nodeCountAccepted) Reject("缺少节点总数。");
                if (nodes.Count != expectedNodeCount)
                    Reject("节点清单不完整：收到 " + nodes.Count + "，应为 " + expectedNodeCount + "。");
                if (probeCompleted.Count != expectedNodeCount)
                    Reject("探测进度不完整：收到 " + probeCompleted.Count + "，应为 " + expectedNodeCount + "。");
                if (results.Count != expectedNodeCount)
                    Reject("节点结果不完整：收到 " + results.Count + "，应为 " + expectedNodeCount + "。");
            }
        }

        private static void Reject(string message)
        {
            throw new InvalidOperationException(message);
        }
    }

    internal sealed class RegionProtocolValidator
    {
        private enum ProtocolPhase
        {
            ExpectProtocol,
            ExpectRegionCount,
            ReadingEvents,
            Complete
        }

        private readonly object sync = new object();
        private readonly HashSet<string> expectedIds;
        private readonly HashSet<string> results = new HashSet<string>(StringComparer.Ordinal);
        private ProtocolPhase phase = ProtocolPhase.ExpectProtocol;

        public RegionProtocolValidator(IEnumerable<string> ids)
        {
            expectedIds = new HashSet<string>(StringComparer.Ordinal);
            foreach (string rawId in ids ?? Enumerable.Empty<string>())
            {
                string id = (rawId ?? "").Trim();
                if (id.Length == 0 || !string.Equals(id, rawId, StringComparison.Ordinal)
                    || !expectedIds.Add(id))
                    throw new InvalidOperationException("地区查询请求包含空白、重复或未规范化的节点 ID。");
            }
            if (expectedIds.Count == 0)
                throw new InvalidOperationException("地区查询请求没有节点 ID。");
        }

        public bool IsComplete
        {
            get
            {
                lock (sync)
                    return phase == ProtocolPhase.Complete;
            }
        }

        public int ResultCount
        {
            get { lock (sync) return results.Count; }
        }

        public static void ValidateEventEnvelope(Dictionary<string, object> value)
        {
            if (value == null || !value.ContainsKey("node_id") || !value.ContainsKey("success")
                || !(value["node_id"] is string) || !(value["success"] is bool))
                Reject("地区事件缺少 node_id、success 或字段类型错误。");
            HashSet<string> allowed = new HashSet<string>(new[]
            {
                "node_id", "success", "country_code", "country", "city",
                "emoji", "provider", "error"
            }, StringComparer.Ordinal);
            foreach (KeyValuePair<string, object> item in value)
            {
                if (!allowed.Contains(item.Key)) Reject("地区事件包含未知字段：" + item.Key);
                if (item.Key != "node_id" && item.Key != "success" && !(item.Value is string))
                    Reject("地区事件可选字段类型错误：" + item.Key);
            }
        }

        public void AcceptProtocol(int version)
        {
            lock (sync)
            {
                if (phase != ProtocolPhase.ExpectProtocol)
                    Reject("地区协议版本行重复或顺序错误。");
                if (version != 2) Reject("仅支持地区事件协议 v2，实际为 v" + version + "。");
                phase = ProtocolPhase.ExpectRegionCount;
            }
        }

        public void AcceptRegionCount(int count)
        {
            lock (sync)
            {
                if (phase != ProtocolPhase.ExpectRegionCount)
                    Reject("地区结果总数重复或顺序错误。");
                if (count != expectedIds.Count)
                    Reject("地区结果声明数量与请求数量不一致。");
                phase = ProtocolPhase.ReadingEvents;
            }
        }

        public void AcceptEvent(NodeRegionEvent value)
        {
            lock (sync)
            {
                if (phase != ProtocolPhase.ReadingEvents)
                    Reject(phase == ProtocolPhase.Complete
                        ? "地区结果已经完整，不能再接收额外事件。"
                        : "地区结果事件出现在声明数量之前。");
                if (value == null || string.IsNullOrWhiteSpace(value.node_id)
                    || !string.Equals(value.node_id, value.node_id.Trim(), StringComparison.Ordinal))
                    Reject("地区结果事件缺少有效节点 ID。");
                if (!expectedIds.Contains(value.node_id))
                    Reject("地区结果引用未知节点 ID：" + value.node_id);
                if (results.Contains(value.node_id))
                    Reject("地区结果重复：" + value.node_id);
                if (value.success)
                {
                    if (!IsCountryCode(value.country_code)
                        || !IsNormalizedNonempty(value.country)
                        || !IsNormalizedOptional(value.city)
                        || !IsNormalizedOptional(value.emoji)
                        || !IsNormalizedNonempty(value.provider)
                        || !string.IsNullOrEmpty(value.error))
                        Reject("成功的地区结果缺少国家代码、国家或提供商。");
                }
                else if (!IsNormalizedNonempty(value.error))
                {
                    Reject("失败的地区结果缺少错误信息。");
                }
                else if (!string.IsNullOrEmpty(value.country_code)
                    || !string.IsNullOrEmpty(value.country)
                    || !string.IsNullOrEmpty(value.city)
                    || !string.IsNullOrEmpty(value.emoji)
                    || !string.IsNullOrEmpty(value.provider))
                {
                    Reject("失败的地区结果不能同时携带成功字段。");
                }
                results.Add(value.node_id);
                if (results.Count == expectedIds.Count) phase = ProtocolPhase.Complete;
            }
        }

        public void ValidateCompletion()
        {
            lock (sync)
            {
                if (phase == ProtocolPhase.ExpectProtocol) Reject("缺少地区协议版本。");
                if (phase == ProtocolPhase.ExpectRegionCount) Reject("缺少地区结果总数。");
                if (phase != ProtocolPhase.Complete)
                    Reject("地区结果不完整：收到 " + results.Count
                        + "，应为 " + expectedIds.Count + "。");
            }
        }

        private static bool IsNormalizedNonempty(string value)
        {
            return !string.IsNullOrEmpty(value)
                && string.Equals(value, value.Trim(), StringComparison.Ordinal);
        }

        private static bool IsCountryCode(string value)
        {
            return value != null && value.Length == 2
                && value[0] >= 'A' && value[0] <= 'Z'
                && value[1] >= 'A' && value[1] <= 'Z';
        }

        private static bool IsNormalizedOptional(string value)
        {
            return string.IsNullOrEmpty(value)
                || string.Equals(value, value.Trim(), StringComparison.Ordinal);
        }

        private static void Reject(string message)
        {
            throw new InvalidOperationException(message);
        }
    }

    internal static class RegionProtocolLineParser
    {
        private static readonly UTF8Encoding StrictUtf8 = new UTF8Encoding(false, true);

        public static NodeRegionEvent AcceptLine(
            string line, RegionProtocolValidator validator, JavaScriptSerializer serializer)
        {
            if (validator == null) throw new ArgumentNullException("validator");
            if (serializer == null) throw new ArgumentNullException("serializer");
            if (string.IsNullOrWhiteSpace(line)) return null;
            string[] parts = line.Split('\t');
            try
            {
                if (line.StartsWith("@protocol\t", StringComparison.Ordinal))
                {
                    int version;
                    if (parts.Length != 2 || !int.TryParse(parts[1], NumberStyles.None,
                        CultureInfo.InvariantCulture, out version))
                        throw new InvalidOperationException("地区协议版本行格式错误。");
                    validator.AcceptProtocol(version);
                    return null;
                }
                if (line.StartsWith("@regions\t", StringComparison.Ordinal))
                {
                    int count;
                    if (parts.Length != 2 || !int.TryParse(parts[1], NumberStyles.None,
                        CultureInfo.InvariantCulture, out count))
                        throw new InvalidOperationException("地区结果总数行格式错误。");
                    validator.AcceptRegionCount(count);
                    return null;
                }
                if (line.StartsWith("@regionjson\t", StringComparison.Ordinal))
                {
                    if (parts.Length != 2 || string.IsNullOrEmpty(parts[1])
                        || parts[1].Length % 4 == 1
                        || !Regex.IsMatch(parts[1], @"\A[A-Za-z0-9+/]+\z"))
                        throw new InvalidOperationException("地区结果事件行格式错误。");
                    int padding = (4 - parts[1].Length % 4) % 4;
                    string json = StrictUtf8.GetString(Convert.FromBase64String(
                        parts[1] + new string('=', padding)));
                    Dictionary<string, object> envelope =
                        serializer.DeserializeObject(json) as Dictionary<string, object>;
                    RegionProtocolValidator.ValidateEventEnvelope(envelope);
                    NodeRegionEvent value = serializer.Deserialize<NodeRegionEvent>(json);
                    validator.AcceptEvent(value);
                    return value;
                }
                if (line.StartsWith("@", StringComparison.Ordinal))
                    throw new InvalidOperationException("未知地区事件：" + parts[0]);
                throw new InvalidOperationException("地区查询器输出了无法识别的文本行。");
            }
            catch (Exception ex)
            {
                throw new InvalidOperationException("地区事件协议错误：" + ex.Message, ex);
            }
        }
    }

    internal enum NodeUiUpdateKind
    {
        Manifest,
        Progress,
        Result
    }

    internal sealed class NodeUiUpdate
    {
        public int OperationId;
        public NodeUiUpdateKind Kind;
        public NodeSnapshot Node;
        public NodeProgressEvent Progress;
        public NodeResultEvent Result;
    }

    internal sealed class NodeUiUpdateQueue
    {
        private readonly object sync = new object();
        private readonly Queue<NodeUiUpdate> updates = new Queue<NodeUiUpdate>();

        public int Count
        {
            get { lock (sync) return updates.Count; }
        }

        public void Enqueue(NodeUiUpdate update)
        {
            if (update == null) return;
            lock (sync) updates.Enqueue(update);
        }

        public List<NodeUiUpdate> Drain(int operationId, int maximum)
        {
            List<NodeUiUpdate> result = new List<NodeUiUpdate>();
            lock (sync)
            {
                int examined = 0;
                while (updates.Count > 0 && examined < maximum)
                {
                    NodeUiUpdate update = updates.Dequeue();
                    examined++;
                    if (update.OperationId == operationId) result.Add(update);
                }
            }
            return result;
        }

        public void Clear()
        {
            lock (sync) updates.Clear();
        }
    }

    internal sealed class NodeRegionEvent
    {
        public string node_id { get; set; }
        public bool success { get; set; }
        public string country_code { get; set; }
        public string country { get; set; }
        public string city { get; set; }
        public string emoji { get; set; }
        public string provider { get; set; }
        public string error { get; set; }
    }

    internal sealed class NodeRegionSnapshot
    {
        public string State;
        public string CountryCode;
        public string Country;
        public string City;
        public string Emoji;
        public string Error;

        public static NodeRegionSnapshot Capture(NodeSnapshot node)
        {
            if (node == null) throw new ArgumentNullException("node");
            return new NodeRegionSnapshot
            {
                State = node.RegionState,
                CountryCode = node.RegionCountryCode,
                Country = node.RegionCountry,
                City = node.RegionCity,
                Emoji = node.RegionEmoji,
                Error = node.RegionError
            };
        }

        public void Restore(NodeSnapshot node)
        {
            if (node == null) throw new ArgumentNullException("node");
            node.RegionState = State;
            node.RegionCountryCode = CountryCode;
            node.RegionCountry = Country;
            node.RegionCity = City;
            node.RegionEmoji = Emoji;
            node.RegionError = Error;
        }
    }

    internal static class RegionEventProjection
    {
        public static void Apply(NodeSnapshot node, NodeRegionEvent value)
        {
            if (node == null) throw new ArgumentNullException("node");
            if (value == null) throw new ArgumentNullException("value");
            if (value.success)
            {
                node.RegionState = "成功";
                node.RegionCountryCode = (value.country_code ?? "").Trim().ToUpperInvariant();
                node.RegionCountry = (value.country ?? "").Trim();
                node.RegionCity = (value.city ?? "").Trim();
                node.RegionEmoji = (value.emoji ?? "").Trim();
                node.RegionError = "";
                return;
            }
            node.RegionState = "查询失败";
            node.RegionCountryCode = "";
            node.RegionCountry = "";
            node.RegionCity = "";
            node.RegionEmoji = "";
            node.RegionError = (value.error ?? "").Trim();
        }
    }

    internal sealed class NodeRegionRequest
    {
        public string[] ids { get; set; }
    }

    internal sealed class NodeManagementRequest
    {
        public Dictionary<string, string> renames { get; set; }
        public string[] deletes { get; set; }
    }

    internal sealed class NodeManagementResult
    {
        public int renamed { get; set; }
        public int deleted { get; set; }
        public NodeManifestEvent[] nodes { get; set; }
    }

    internal static class NodeNamePolicy
    {
        public static string Normalize(string value)
        {
            string name = (value ?? "").Trim();
            if (name.Length == 0) throw new InvalidOperationException("节点名称不能为空。");
            if (name.Any(delegate(char character) { return char.IsControl(character); }))
                throw new InvalidOperationException("节点名称不能包含换行或其他控制字符。");
            return name;
        }
    }

    internal sealed class NodeEditDialog : Form
    {
        private readonly TextBox nameText;

        public string NodeName
        {
            get { return NodeNamePolicy.Normalize(nameText.Text); }
        }

        public NodeEditDialog(string currentName, string nodeType)
        {
            Text = "重命名节点";
            Name = "NodeEditDialog";
            StartPosition = FormStartPosition.CenterParent;
            FormBorderStyle = FormBorderStyle.FixedDialog;
            ClientSize = new Size(520, 175);
            MaximizeBox = false;
            MinimizeBox = false;
            ShowInTaskbar = false;
            Font = new Font("Microsoft YaHei UI", 9F);
            AutoScaleMode = AutoScaleMode.Dpi;
            AutoScaleDimensions = new SizeF(96F, 96F);
            KeyPreview = true;

            Label description = new Label
            {
                Text = "名称会写入本地筛选结果，但不会修改服务器、端口、凭据或原订阅。",
                Location = new Point(18, 16),
                Size = new Size(480, 28)
            };
            Label typeLabel = new Label
            {
                Text = "协议类型：" + (nodeType ?? ""),
                Location = new Point(18, 52),
                Size = new Size(480, 23)
            };
            Label nameLabel = new Label
            {
                Text = "节点名称：",
                Location = new Point(18, 84),
                Size = new Size(78, 25),
                TextAlign = ContentAlignment.MiddleLeft
            };
            nameText = new TextBox
            {
                Name = "NodeNameInput",
                Text = currentName ?? "",
                Location = new Point(98, 84),
                Size = new Size(400, 25),
                MaxLength = 256
            };
            Button saveButton = new Button
            {
                Name = "SaveNodeNameButton",
                Text = "保存",
                DialogResult = DialogResult.OK,
                Location = new Point(338, 129),
                Size = new Size(76, 28)
            };
            Button cancelButton = new Button
            {
                Name = "CancelNodeNameButton",
                Text = "取消",
                DialogResult = DialogResult.Cancel,
                Location = new Point(422, 129),
                Size = new Size(76, 28)
            };
            AcceptButton = saveButton;
            CancelButton = cancelButton;
            Controls.Add(description);
            Controls.Add(typeLabel);
            Controls.Add(nameLabel);
            Controls.Add(nameText);
            Controls.Add(saveButton);
            Controls.Add(cancelButton);
            Shown += delegate
            {
                nameText.SelectAll();
                nameText.Focus();
            };
            FormClosing += delegate(object sender, FormClosingEventArgs e)
            {
                if (DialogResult != DialogResult.OK) return;
                try
                {
                    NodeNamePolicy.Normalize(nameText.Text);
                }
                catch (Exception ex)
                {
                    e.Cancel = true;
                    MessageBox.Show(this, ex.Message, "名称无效",
                        MessageBoxButtons.OK, MessageBoxIcon.Warning);
                }
            };
        }
    }

    internal sealed class NodeRowComparer : IComparer
    {
        private readonly string header;
        private readonly ListSortDirection direction;
        private readonly bool defaultOrder;

        public NodeRowComparer(string header, ListSortDirection direction, bool defaultOrder)
        {
            this.header = header;
            this.direction = direction;
            this.defaultOrder = defaultOrder;
        }

        public int Compare(object left, object right)
        {
            DataGridViewRow leftRow = left as DataGridViewRow;
            DataGridViewRow rightRow = right as DataGridViewRow;
            NodeSnapshot leftNode = leftRow == null ? null : leftRow.Tag as NodeSnapshot;
            NodeSnapshot rightNode = rightRow == null ? null : rightRow.Tag as NodeSnapshot;
            int result;
            if (defaultOrder)
            {
                result = CompareStatus(leftNode, rightNode);
                if (result == 0) result = CompareLatency(leftNode, rightNode);
                if (result == 0) result = CompareText(GetName(leftNode), GetName(rightNode));
                return result;
            }

            switch (header)
            {
                case "HTTP 延迟":
                    return CompareMeasured(leftNode == null ? 0 : leftNode.LatencyMs,
                        leftNode != null && leftNode.LatencyMs > 0,
                        rightNode == null ? 0 : rightNode.LatencyMs,
                        rightNode != null && rightNode.LatencyMs > 0,
                        leftNode, rightNode);
                case "下载速度":
                    return CompareMeasured(leftNode == null ? 0 : leftNode.DownloadMbps,
                        leftNode != null && leftNode.DownloadTested && leftNode.DownloadMbps > 0,
                        rightNode == null ? 0 : rightNode.DownloadMbps,
                        rightNode != null && rightNode.DownloadTested && rightNode.DownloadMbps > 0,
                        leftNode, rightNode);
                case "状态":
                    result = CompareStatus(leftNode, rightNode);
                    break;
                case "协议类型":
                case "类型":
                    result = CompareText(leftNode == null ? "" : leftNode.Type,
                        rightNode == null ? "" : rightNode.Type);
                    break;
                case "出口地区":
                    result = CompareText(RegionFormatter.Format(leftNode),
                        RegionFormatter.Format(rightNode));
                    break;
                case "节点名称":
                    result = CompareText(GetName(leftNode), GetName(rightNode));
                    break;
                default:
                    result = CompareCell(leftRow, rightRow, header);
                    break;
            }
            if (result == 0) result = CompareText(GetName(leftNode), GetName(rightNode));
            return direction == ListSortDirection.Descending ? -result : result;
        }

        private static int CompareLatency(NodeSnapshot left, NodeSnapshot right)
        {
            double leftValue = left == null || left.LatencyMs <= 0 ? double.MaxValue : left.LatencyMs;
            double rightValue = right == null || right.LatencyMs <= 0 ? double.MaxValue : right.LatencyMs;
            return leftValue.CompareTo(rightValue);
        }

        private int CompareMeasured(double leftValue, bool leftMeasured,
            double rightValue, bool rightMeasured, NodeSnapshot left, NodeSnapshot right)
        {
            if (leftMeasured != rightMeasured) return leftMeasured ? -1 : 1;
            int result = leftMeasured ? leftValue.CompareTo(rightValue) : 0;
            if (direction == ListSortDirection.Descending) result = -result;
            if (result == 0) result = CompareText(GetName(left), GetName(right));
            return result;
        }

        private static int CompareStatus(NodeSnapshot left, NodeSnapshot right)
        {
            return StatusRank(left == null ? "" : left.State)
                .CompareTo(StatusRank(right == null ? "" : right.State));
        }

        private static int StatusRank(string value)
        {
            if (value == "有效" || value == "通过") return 0;
            if (value == "等待") return 1;
            return 2;
        }

        private static int CompareCell(DataGridViewRow left, DataGridViewRow right, string header)
        {
            if (left == null || right == null) return 0;
            DataGridViewColumn column = left.DataGridView == null
                ? null
                : left.DataGridView.Columns.Cast<DataGridViewColumn>()
                    .FirstOrDefault(delegate(DataGridViewColumn value) { return value.HeaderText == header; });
            if (column == null) return 0;
            return CompareText(Convert.ToString(left.Cells[column.Index].Value, CultureInfo.CurrentCulture),
                Convert.ToString(right.Cells[column.Index].Value, CultureInfo.CurrentCulture));
        }

        private static int CompareText(string left, string right)
        {
            return StringComparer.CurrentCultureIgnoreCase.Compare(left ?? "", right ?? "");
        }

        private static string GetName(NodeSnapshot node)
        {
            return node == null ? "" : node.Name;
        }
    }

    internal static class RegionFormatter
    {
        public static string Format(NodeSnapshot node)
        {
            if (node == null) return "—";
            string state = node.RegionState ?? "—";
            if (!string.Equals(state, "成功", StringComparison.Ordinal)) return state;
            string country = (node.RegionCountry ?? "").Trim();
            string city = (node.RegionCity ?? "").Trim();
            string place = country;
            if (city.Length > 0 && !string.Equals(city, country, StringComparison.CurrentCultureIgnoreCase))
                place = country.Length == 0 ? city : country + "·" + city;
            string emoji = (node.RegionEmoji ?? "").Trim();
            return (emoji.Length == 0 ? "" : emoji + " ") + place;
        }

        public static string Ellipsize(string value, int maxTextElements)
        {
            value = value ?? "";
            if (maxTextElements < 2) return value;
            int[] elements = StringInfo.ParseCombiningCharacters(value);
            if (elements.Length <= maxTextElements) return value;
            return value.Substring(0, elements[maxTextElements - 1]) + "…";
        }
    }

    internal static class NodeListPresentation
    {
        public static readonly string[] Headers =
        {
            "序号", "节点名称", "类型", "HTTP 延迟", "下载速度", "出口地区", "状态"
        };
        public static string MetricText(bool tested, string value)
        {
            return tested && !string.IsNullOrWhiteSpace(value) ? value : "未测试";
        }

        public static string TransferMetricText(bool tested, bool complete, string value)
        {
            if (!tested) return "未测试";
            if (!complete && (string.IsNullOrWhiteSpace(value)
                || string.Equals(value.Trim(), "未测试", StringComparison.Ordinal)
                || string.Equals(value.Trim(), "N/A", StringComparison.OrdinalIgnoreCase)
                || string.Equals(value.Trim(), "—", StringComparison.Ordinal)))
                return "传输未完成";
            return string.IsNullOrWhiteSpace(value) ? "未测试" : value;
        }

        public static string StatusText(NodeSnapshot node)
        {
            if (node == null) return "";
            if (string.Equals(node.State, "失败", StringComparison.Ordinal)
                && node.DownloadTested && !node.DownloadComplete)
                return "失败（传输未完成）";
            return node.State ?? "";
        }

        public static object[] ProtocolFilterOptions(IEnumerable<NodeSnapshot> nodes)
        {
            List<object> options = new List<object> { "全部" };
            foreach (string protocol in (nodes ?? Enumerable.Empty<NodeSnapshot>())
                .Where(delegate(NodeSnapshot node)
                {
                    return node != null && !string.IsNullOrWhiteSpace(node.Type);
                })
                .Select(delegate(NodeSnapshot node) { return ProtocolDisplayName(node.Type); })
                .Distinct(StringComparer.OrdinalIgnoreCase)
                .OrderBy(delegate(string value) { return value; }, StringComparer.OrdinalIgnoreCase))
            {
                options.Add(protocol);
            }
            return options.ToArray();
        }

        public static object[] RegionFilterOptions(IEnumerable<NodeSnapshot> nodes)
        {
            List<object> options = new List<object> { "全部" };
            IEnumerable<IGrouping<string, NodeSnapshot>> countries =
                (nodes ?? Enumerable.Empty<NodeSnapshot>())
                    .Where(delegate(NodeSnapshot node)
                    {
                        return node != null
                            && string.Equals(node.RegionState, "成功", StringComparison.Ordinal)
                            && IsCountryCode(node.RegionCountryCode);
                    })
                    .GroupBy(delegate(NodeSnapshot node)
                    {
                        return node.RegionCountryCode.Trim().ToUpperInvariant();
                    }, StringComparer.OrdinalIgnoreCase)
                    .OrderBy(delegate(IGrouping<string, NodeSnapshot> group) { return group.Key; },
                        StringComparer.OrdinalIgnoreCase);
            foreach (IGrouping<string, NodeSnapshot> country in countries)
            {
                string name = country.Select(delegate(NodeSnapshot node)
                    {
                        return (node.RegionCountry ?? "").Trim();
                    })
                    .Where(delegate(string value) { return value.Length > 0; })
                    .OrderBy(delegate(string value) { return value; }, StringComparer.Ordinal)
                    .FirstOrDefault() ?? "";
                options.Add(country.Key + (name.Length == 0 ? "" : " " + name));
            }
            return options.ToArray();
        }

        private static bool IsCountryCode(string value)
        {
            value = (value ?? "").Trim();
            return value.Length == 2 && value.All(delegate(char character)
            {
                char upper = char.ToUpperInvariant(character);
                return upper >= 'A' && upper <= 'Z';
            });
        }

        private static string ProtocolDisplayName(string value)
        {
            string protocol = (value ?? "").Trim();
            switch (protocol.ToLowerInvariant())
            {
                case "ss":
                case "shadowsocks": return "SS";
                case "ssr": return "SSR";
                case "vmess": return "VMess";
                case "vless": return "VLESS";
                case "trojan": return "Trojan";
                case "hysteria": return "Hysteria";
                case "hysteria2":
                case "hy2": return "Hysteria2";
                case "tuic": return "TUIC";
                case "anytls": return "AnyTLS";
                case "http": return "HTTP";
                case "socks": return "SOCKS";
                case "socks5": return "SOCKS5";
                default: return protocol;
            }
        }

        public static string NextVisibleIndex(ref int index)
        {
            index++;
            return index.ToString(CultureInfo.InvariantCulture);
        }
    }

    internal static class StatusStatistics
    {
        public static string Format(int total, int visible, int selected,
            int valid, int failed, int waiting)
        {
            return "总数 " + total + " | 筛选后 " + visible
                + " | 已选 " + selected + " | 有效 " + valid
                + " | 失败 " + failed + " | 等待 " + waiting;
        }
    }

    internal static class NodeListSelection
    {
        public static List<DataGridViewRow> GetSelectedVisibleRows(DataGridView grid)
        {
            if (grid == null) return new List<DataGridViewRow>();
            return grid.SelectedRows.Cast<DataGridViewRow>()
                .Where(delegate(DataGridViewRow row) { return row.Visible && !row.IsNewRow; })
                .ToList();
        }

        public static void SelectAllVisibleRows(DataGridView grid)
        {
            if (grid == null) return;
            grid.ClearSelection();
            foreach (DataGridViewRow row in grid.Rows)
            {
                if (row.Visible && !row.IsNewRow) row.Selected = true;
            }
        }
    }

    internal static class NodeMetaFormatter
    {
        public static string Format(IEnumerable<NodeSnapshot> nodes)
        {
            JavaScriptSerializer serializer = new JavaScriptSerializer { MaxJsonLength = int.MaxValue };
            StringBuilder output = new StringBuilder("proxies:");
            foreach (NodeSnapshot node in nodes)
            {
                if (node == null || node.Config == null)
                    throw new InvalidOperationException("节点缺少完整配置。");
                output.AppendLine();
                output.Append("  - ");
                output.Append(serializer.Serialize(node.Config));
            }
            return output.ToString();
        }
    }

    internal sealed class MainForm : Form
    {
        private static readonly UTF8Encoding StrictEventUtf8 = new UTF8Encoding(false, true);
        private const int BasicOptionsHeight = 421;
        private const int ExpandedOptionsContentHeight = 635;
        private const int OptionsContentWidth = 1150;
        private const int MinimumResultGridHeight = 170;
        private readonly string baseDirectory;
        private readonly string parserPath;
        private readonly string runnerPath;
        private readonly Panel optionsPanel;
        private readonly GroupBox advancedGroup;
        private readonly DataGridView resultGrid;
        private readonly StatusStrip statusStrip;
        private readonly ToolStripStatusLabel statusLabel;
        private readonly ToolStripStatusLabel statisticsLabel;
        private readonly List<Control> taskConfigurationControls = new List<Control>();
        private readonly NodeUiUpdateQueue nodeUiUpdates = new NodeUiUpdateQueue();
        private readonly System.Windows.Forms.Timer nodeUiTimer;

        private TextBox configText;
        private TextBox filterText;
        private NumericUpDown latencyNumber;
        private NumericUpDown downloadSpeedNumber;
        private TextBox outputText;
        private CheckBox renameCheck;
        private CheckBox gistCheck;
        private TextBox gistUsernameText;
        private TextBox gistTokenText;
        private Button tokenEyeButton;
        private Button startButton;
        private Button stopButton;
        private Button advancedButton;
        private Button openOutputButton;
        private Button copyOutputButton;
        private Button queryRegionButton;
        private ComboBox presetCombo;
        private Label presetHintLabel;
        private ComboBox statusFilterCombo;
        private ComboBox latencyFilterCombo;
        private ComboBox protocolFilterCombo;
        private ComboBox regionFilterCombo;
        private bool applyingPreset;
        private bool applyingNodeListFilters;

        private ComboBox modeCombo;
        private TextBox blockText;
        private TextBox serverUrlText;
        private NumericUpDown downloadSizeNumber;
        private NumericUpDown probeTimeoutNumber;
        private NumericUpDown timeoutNumber;
        private NumericUpDown concurrentNumber;
        private NumericUpDown transferConcurrentNumber;
        private NumericUpDown probeFailureNumber;
        private TextBox userAgentText;

        private AppSettings settings;
        private TaskOperation activeOperation;
        private int nextOperationId;
        private bool closeWhenOperationEnds;
        private bool allowClose;
        private bool runInProgress;
        private bool managementInProgress;
        private bool regionQueryInProgress;
        private int regionQueryTotal;
        private int regionQueryCompleted;
        private int regionQuerySuccess;
        private int regionQueryFailed;
        private readonly HashSet<string> regionQueryPendingIds =
            new HashSet<string>(StringComparer.Ordinal);
        private List<string> currentHeaders = new List<string>();
        private RunOptions activeOptions;
        private int parsedRowCount;
        private int passedRowCount;
        private int manifestRowCount;
        private int displayedResultCount;
        private int displayedPassedCount;
        private int displayedProbeCount;
        private string displayedDownloadingNode = "";
        private bool userSelectedSort;
        private string activeSortHeader = "";
        private ListSortDirection activeSortDirection = ListSortDirection.Ascending;
        private bool suppressStatisticsEvents;
        private readonly JavaScriptSerializer nodeSerializer =
            new JavaScriptSerializer { MaxJsonLength = int.MaxValue };
        private readonly Dictionary<string, NodeSnapshot> nodesById =
            new Dictionary<string, NodeSnapshot>(StringComparer.Ordinal);
        private readonly Dictionary<string, DataGridViewRow> nodeRows =
            new Dictionary<string, DataGridViewRow>(StringComparer.Ordinal);

        public MainForm()
        {
            baseDirectory = AppDomain.CurrentDomain.BaseDirectory;
            parserPath = Path.Combine(baseDirectory, "subscription-parser.exe");
            runnerPath = Path.Combine(baseDirectory, "speedtest-runner.exe");
            settings = SettingsStore.Load();
            nodeUiTimer = new System.Windows.Forms.Timer { Interval = 50 };
            nodeUiTimer.Tick += delegate { FlushPendingNodeUiEvents(200); };

            Text = "Clash-SpeedTest 图形界面";
            Name = "MainWindow";
            try { Icon = Icon.ExtractAssociatedIcon(Application.ExecutablePath); } catch { }
            StartPosition = FormStartPosition.CenterScreen;
            Size = new Size(1180, 820);
            MinimumSize = new Size(980, 700);
            Font = new Font("Microsoft YaHei UI", 9F);
            AutoScaleMode = AutoScaleMode.Dpi;
            AutoScaleDimensions = new SizeF(96F, 96F);

            optionsPanel = new Panel
            {
                Name = "OptionsPanel",
                TabIndex = 0,
                Dock = DockStyle.Top,
                AutoScroll = true,
                BackColor = Color.FromArgb(248, 249, 251)
            };

            resultGrid = new DataGridView
            {
                Name = "ResultGrid",
                TabIndex = 1,
                Dock = DockStyle.Fill,
                ReadOnly = true,
                AllowUserToAddRows = false,
                AllowUserToDeleteRows = false,
                AllowUserToOrderColumns = true,
                AutoSizeColumnsMode = DataGridViewAutoSizeColumnsMode.Fill,
                BackgroundColor = Color.White,
                BorderStyle = BorderStyle.None,
                RowHeadersVisible = false,
                SelectionMode = DataGridViewSelectionMode.FullRowSelect,
                MultiSelect = true,
                ClipboardCopyMode = DataGridViewClipboardCopyMode.Disable
            };
            resultGrid.KeyDown += OnResultGridKeyDown;
            resultGrid.CellMouseDown += OnResultGridCellMouseDown;
            resultGrid.ColumnHeaderMouseClick += OnResultGridColumnHeaderMouseClick;
            resultGrid.SelectionChanged += delegate
            {
                if (!suppressStatisticsEvents) UpdateStatistics();
            };
            resultGrid.ContextMenuStrip = BuildNodeContextMenu();

            statusStrip = new StatusStrip { Name = "StatusStrip" };
            statusLabel = new ToolStripStatusLabel("就绪")
            {
                Name = "StatusText",
                Spring = true,
                TextAlign = ContentAlignment.MiddleLeft
            };
            statisticsLabel = new ToolStripStatusLabel { Name = "StatisticsText" };
            statusStrip.Items.Add(statusLabel);
            statusStrip.Items.Add(statisticsLabel);

            Controls.Add(resultGrid);
            Controls.Add(optionsPanel);
            Controls.Add(statusStrip);

            BuildBasicControls();
            AcceptButton = startButton;
            CancelButton = stopButton;
            advancedGroup = BuildAdvancedControls();
            optionsPanel.Controls.Add(advancedGroup);
            taskConfigurationControls.Add(advancedGroup);
            LoadSettingsToControls();
            ApplyAdvancedState(settings.AdvancedExpanded);

            FormClosing += OnFormClosing;
            Shown += OnShown;
            Resize += delegate { UpdateOptionsPanelLayout(); };
        }

        protected override bool ProcessCmdKey(ref Message msg, Keys keyData)
        {
            if (keyData == Keys.Escape && !IsComboDropDownOpen()
                && stopButton != null && stopButton.Enabled)
            {
                stopButton.PerformClick();
                return true;
            }
            if (keyData == Keys.F5 && startButton != null && startButton.Enabled)
            {
                startButton.PerformClick();
                return true;
            }
            if (keyData == Keys.F6
                && queryRegionButton != null && queryRegionButton.Enabled)
            {
                queryRegionButton.PerformClick();
                return true;
            }
            return base.ProcessCmdKey(ref msg, keyData);
        }

        private bool IsComboDropDownOpen()
        {
            return FindDroppedDownCombo(this) != null;
        }

        private static ComboBox FindDroppedDownCombo(Control root)
        {
            foreach (Control control in root.Controls)
            {
                ComboBox combo = control as ComboBox;
                if (combo != null && combo.DroppedDown) return combo;
                ComboBox nested = FindDroppedDownCombo(control);
                if (nested != null) return nested;
            }
            return null;
        }

        private ContextMenuStrip BuildNodeContextMenu()
        {
            ContextMenuStrip menu = new ContextMenuStrip();
            ToolStripMenuItem copyUrl = new ToolStripMenuItem("复制节点 URL");
            ToolStripMenuItem copyMeta = new ToolStripMenuItem("复制节点 Clash Meta");
            ToolStripMenuItem copyName = new ToolStripMenuItem("复制节点名称");
            ToolStripMenuItem edit = new ToolStripMenuItem("重命名节点");
            ToolStripMenuItem delete = new ToolStripMenuItem("从结果中删除节点");
            ToolStripMenuItem queryRegion = new ToolStripMenuItem("查询/刷新选中节点出口地区");
            copyUrl.Name = "CopyNodeUrlMenuItem";
            copyMeta.Name = "CopyNodeMetaMenuItem";
            copyName.Name = "CopyNodeNameMenuItem";
            edit.Name = "RenameNodeMenuItem";
            delete.Name = "DeleteNodeMenuItem";
            queryRegion.Name = "QuerySelectedRegionsMenuItem";
            copyUrl.Click += delegate { CopySelectedNodeUrls(); };
            copyMeta.Click += delegate { CopySelectedNodeMeta(); };
            copyName.Click += delegate { CopySelectedNodeNames(); };
            edit.Click += OnEditNodeClick;
            delete.Click += OnDeleteNodesClick;
            queryRegion.Click += async delegate
            {
                await QueryRegionsAsync(GetSelectedNodes(), true);
            };
            menu.Items.Add(copyUrl);
            menu.Items.Add(copyMeta);
            menu.Items.Add(copyName);
            menu.Items.Add(new ToolStripSeparator());
            menu.Items.Add(edit);
            menu.Items.Add(delete);
            menu.Items.Add(new ToolStripSeparator());
            menu.Items.Add(queryRegion);
            menu.Opening += delegate(object sender, CancelEventArgs e)
            {
                bool available = GetSelectedNodes().Count > 0;
                copyUrl.Enabled = available;
                copyMeta.Enabled = available;
                copyName.Enabled = available;
                bool idle = !runInProgress && !managementInProgress && !regionQueryInProgress;
                List<NodeSnapshot> selected = GetSelectedNodes();
                bool outputAvailable = HasManagedOutput();
                edit.Enabled = idle && outputAvailable && selected.Count == 1 && selected[0].Exported;
                delete.Enabled = idle && outputAvailable && selected.Count > 0
                    && selected.All(delegate(NodeSnapshot node) { return node.Exported; });
                queryRegion.Enabled = idle && outputAvailable && selected.Count > 0
                    && selected.All(delegate(NodeSnapshot node)
                    {
                        return node.Exported && string.Equals(node.State, "有效", StringComparison.Ordinal);
                    });
                edit.ToolTipText = edit.Enabled
                    ? "重命名本地结果中的节点，不改变连接参数。"
                    : "请选择一个已导出的节点；测速或地区查询期间不能重命名。";
                delete.ToolTipText = delete.Enabled
                    ? "从本地结果中删除所选节点；不会修改原订阅。"
                    : "只能删除已导出到本地结果的节点。";
                queryRegion.ToolTipText = queryRegion.Enabled
                    ? "通过每个选中节点的代理重新查询出口地区。"
                    : "只能查询已导出的有效节点；其他任务期间不可查询。";
            };
            return menu;
        }

        private void OnResultGridKeyDown(object sender, KeyEventArgs e)
        {
            if (e.Control && e.KeyCode == Keys.A)
            {
                bool previousSuppression = suppressStatisticsEvents;
                suppressStatisticsEvents = true;
                try
                {
                    NodeListSelection.SelectAllVisibleRows(resultGrid);
                }
                finally
                {
                    suppressStatisticsEvents = previousSuppression;
                }
                UpdateStatistics();
                e.Handled = true;
                e.SuppressKeyPress = true;
                return;
            }
            if (e.Control && e.KeyCode == Keys.C && GetSelectedNodes().Count > 0)
            {
                CopySelectedNodeUrls();
                e.Handled = true;
                e.SuppressKeyPress = true;
                return;
            }
            if (e.KeyCode == Keys.F2 && GetSelectedNodes().Count == 1)
            {
                OnEditNodeClick(this, EventArgs.Empty);
                e.Handled = true;
                e.SuppressKeyPress = true;
                return;
            }
            if (e.KeyCode == Keys.Delete && GetSelectedNodes().Count > 0)
            {
                OnDeleteNodesClick(this, EventArgs.Empty);
                e.Handled = true;
                e.SuppressKeyPress = true;
            }
        }

        private void OnResultGridCellMouseDown(object sender, DataGridViewCellMouseEventArgs e)
        {
            if (e.Button != MouseButtons.Right) return;
            resultGrid.Focus();
            if (e.RowIndex < 0)
            {
                resultGrid.ClearSelection();
                return;
            }
            DataGridViewRow row = resultGrid.Rows[e.RowIndex];
            if (!row.Selected)
            {
                resultGrid.ClearSelection();
                row.Selected = true;
                int columnIndex = e.ColumnIndex >= 0 ? e.ColumnIndex : 0;
                if (columnIndex < row.Cells.Count)
                    resultGrid.CurrentCell = row.Cells[columnIndex];
            }
        }

        private void OnResultGridColumnHeaderMouseClick(object sender, DataGridViewCellMouseEventArgs e)
        {
            if (e.Button != MouseButtons.Left) return;
            if (e.ColumnIndex < 0 || e.ColumnIndex >= resultGrid.Columns.Count) return;
            DataGridViewColumn column = resultGrid.Columns[e.ColumnIndex];
            if (column.SortMode == DataGridViewColumnSortMode.NotSortable) return;
            ListSortDirection direction = column.HeaderCell.SortGlyphDirection == SortOrder.Ascending
                ? ListSortDirection.Descending
                : ListSortDirection.Ascending;
            activeSortHeader = column.HeaderText;
            activeSortDirection = direction;
            userSelectedSort = true;
            try
            {
                ApplyActiveUserSort();
                ReindexVisibleRows();
            }
            catch (Exception ex)
            {
                userSelectedSort = false;
                activeSortHeader = "";
                SetStatus("排序失败：" + ex.Message);
            }
        }

        private void ApplyActiveUserSort()
        {
            if (!userSelectedSort || string.IsNullOrWhiteSpace(activeSortHeader)) return;
            DataGridViewColumn column = resultGrid.Columns.Cast<DataGridViewColumn>()
                .FirstOrDefault(delegate(DataGridViewColumn item)
                {
                    return string.Equals(item.HeaderText, activeSortHeader, StringComparison.Ordinal);
                });
            if (column == null) return;
            resultGrid.Sort(new NodeRowComparer(activeSortHeader, activeSortDirection, false));
            foreach (DataGridViewColumn item in resultGrid.Columns)
                item.HeaderCell.SortGlyphDirection = SortOrder.None;
            column.HeaderCell.SortGlyphDirection = activeSortDirection == ListSortDirection.Ascending
                ? SortOrder.Ascending
                : SortOrder.Descending;
        }

        private List<NodeSnapshot> GetSelectedNodes()
        {
            return NodeListSelection.GetSelectedVisibleRows(resultGrid)
                .OrderBy(delegate(DataGridViewRow row) { return row.Index; })
                .Select(delegate(DataGridViewRow row) { return row.Tag as NodeSnapshot; })
                .Where(delegate(NodeSnapshot node) { return node != null; })
                .ToList();
        }

        private void CopySelectedNodeUrls()
        {
            List<NodeSnapshot> nodes = GetSelectedNodes();
            if (nodes.Count == 0) return;
            List<NodeSnapshot> unavailable = nodes.Where(delegate(NodeSnapshot node)
            {
                return string.IsNullOrWhiteSpace(node.ShareUrl);
            }).ToList();
            if (unavailable.Count > 0)
            {
                string names = string.Join("、", unavailable.Take(5)
                    .Select(delegate(NodeSnapshot node) { return node.Name; }).ToArray());
                if (unavailable.Count > 5) names += " 等 " + unavailable.Count + " 个节点";
                string reason = unavailable[0].ShareError;
                MessageBox.Show(this,
                    "以下节点不能安全生成分享 URL，因此没有修改剪贴板：\n\n" + names
                    + (string.IsNullOrWhiteSpace(reason) ? "" : "\n\n原因：" + reason),
                    "无法复制节点 URL", MessageBoxButtons.OK, MessageBoxIcon.Warning);
                return;
            }
            SetNodeClipboard(string.Join(Environment.NewLine,
                nodes.Select(delegate(NodeSnapshot node) { return node.ShareUrl; }).ToArray()),
                "已复制 " + nodes.Count + " 个节点 URL");
        }

        private void CopySelectedNodeMeta()
        {
            List<NodeSnapshot> nodes = GetSelectedNodes();
            if (nodes.Count == 0) return;
            if (nodes.Any(delegate(NodeSnapshot node) { return node.Config == null; }))
            {
                MessageBox.Show(this, "选中节点缺少完整配置，未修改剪贴板。",
                    "无法复制 Clash Meta", MessageBoxButtons.OK, MessageBoxIcon.Warning);
                return;
            }
            SetNodeClipboard(NodeMetaFormatter.Format(nodes),
                "已复制 " + nodes.Count + " 个节点的 Clash Meta 配置");
        }

        private void CopySelectedNodeNames()
        {
            List<NodeSnapshot> nodes = GetSelectedNodes();
            if (nodes.Count == 0) return;
            SetNodeClipboard(string.Join(Environment.NewLine,
                nodes.Select(delegate(NodeSnapshot node) { return node.Name; }).ToArray()),
                "已复制 " + nodes.Count + " 个节点名称");
        }

        private void SetNodeClipboard(string value, string status)
        {
            try
            {
                Clipboard.SetText(value);
                SetStatus(status);
            }
            catch (Exception ex)
            {
                MessageBox.Show(this, ex.Message, "无法写入剪贴板",
                    MessageBoxButtons.OK, MessageBoxIcon.Error);
            }
        }

        private async void OnEditNodeClick(object sender, EventArgs e)
        {
            await EditSelectedNodeAsync();
        }

        private async void OnDeleteNodesClick(object sender, EventArgs e)
        {
            await DeleteSelectedNodesAsync();
        }

        private async Task EditSelectedNodeAsync()
        {
            if (allowClose || closeWhenOperationEnds
                || runInProgress || managementInProgress || regionQueryInProgress) return;
            List<NodeSnapshot> selected = GetSelectedNodes();
            if (selected.Count != 1 || !selected[0].Exported || !HasManagedOutput()) return;
            string selectedId = selected[0].Id;
            TaskOperation operation = null;
            try
            {
                operation = BeginTaskOperation(TaskOperationKind.NodeManagement);
                string outputPath = ResolveManagedOutputPath();
                NodeSnapshot node;
                if (!nodesById.TryGetValue(selectedId, out node))
                    throw new InvalidOperationException("该节点已不在当前列表中，请重新选择。 ");
                NodeManagementResult current = await Task.Run(delegate
                {
                    return ListManagedNodes(outputPath, operation.Token);
                }, operation.Token);
                operation.Token.ThrowIfCancellationRequested();
                SyncManagedNodes(current.nodes);
                if (!nodesById.TryGetValue(selectedId, out node) || !node.Exported)
                    throw new InvalidOperationException(
                        "该节点已不在当前本地结果中，请重新测速或选择其他节点。");

                string newName;
                UseWaitCursor = false;
                using (NodeEditDialog dialog = new NodeEditDialog(node.Name, node.Type))
                {
                    if (dialog.ShowDialog(this) != DialogResult.OK) return;
                    newName = dialog.NodeName;
                }
                bool nameChanged = !string.Equals(newName, node.Name, StringComparison.Ordinal);
                if (!nameChanged)
                {
                    SetStatus("节点名称未改变");
                    return;
                }
                UseWaitCursor = true;
                operation.Token.ThrowIfCancellationRequested();

                NodeManagementRequest request = new NodeManagementRequest
                {
                    renames = new Dictionary<string, string> { { node.Id, newName } },
                    deletes = new string[0]
                };
                NodeManagementResult result = await Task.Run(delegate
                {
                    return RunNodeManagement(outputPath, request, operation.Token);
                }, operation.Token);
                operation.OutputCommitted = true;
                operation.Token.ThrowIfCancellationRequested();
                SyncManagedNodes(result.nodes);
                passedRowCount = result.nodes == null ? 0 : result.nodes.Length;

                ApplyNodeFilters();
                ApplyDefaultNodeSort();
                await FinishNodeManagementAsync(
                    "已重命名节点为“" + newName + "”", outputPath, operation);
            }
            catch (OperationCanceledException)
            {
                SetStatus(operation != null && operation.OutputCommitted
                    ? "节点管理后续处理已停止；本地结果已保存"
                    : "节点管理已停止；本地结果未修改");
            }
            catch (Exception ex)
            {
                if (operation != null && operation.IsCancellationRequested)
                {
                    SetStatus(operation.OutputCommitted
                        ? "节点管理后续处理已停止；本地结果已保存"
                        : "节点管理已停止；本地结果未修改");
                    return;
                }
                MessageBox.Show(this, ex.Message, "重命名节点失败",
                    MessageBoxButtons.OK, MessageBoxIcon.Error);
                SetStatus("重命名节点失败：" + ex.Message);
            }
            finally
            {
                UseWaitCursor = false;
                if (operation != null) EndTaskOperation(operation);
            }
        }

        private async Task DeleteSelectedNodesAsync()
        {
            if (allowClose || closeWhenOperationEnds
                || runInProgress || managementInProgress || regionQueryInProgress) return;
            List<NodeSnapshot> selected = GetSelectedNodes();
            if (selected.Count == 0 || !HasManagedOutput()
                || selected.Any(delegate(NodeSnapshot node) { return !node.Exported; })) return;
            string[] selectedIds = selected.Select(delegate(NodeSnapshot node) { return node.Id; }).ToArray();
            TaskOperation operation = null;
            try
            {
                operation = BeginTaskOperation(TaskOperationKind.NodeManagement);
                string outputPath = ResolveManagedOutputPath();
                NodeManagementResult current = await Task.Run(delegate
                {
                    return ListManagedNodes(outputPath, operation.Token);
                }, operation.Token);
                operation.Token.ThrowIfCancellationRequested();
                SyncManagedNodes(current.nodes);
                List<NodeSnapshot> currentSelection = selectedIds
                    .Where(delegate(string id)
                    {
                        NodeSnapshot node;
                        return nodesById.TryGetValue(id, out node) && node.Exported;
                    })
                    .Select(delegate(string id) { return nodesById[id]; })
                    .ToList();
                if (currentSelection.Count != selectedIds.Length)
                    throw new InvalidOperationException("部分节点已不在当前本地结果中，请重新选择后再删除。");

                string names = string.Join("、", currentSelection.Take(5)
                    .Select(delegate(NodeSnapshot node) { return node.Name; }).ToArray());
                if (currentSelection.Count > 5) names += " 等 " + currentSelection.Count + " 个节点";
                UseWaitCursor = false;
                DialogResult confirmation = MessageBox.Show(this,
                    "将从本地筛选结果中删除以下节点：\n\n" + names
                    + "\n\n原订阅不会被修改。删除后如重新测速，这些节点仍可能再次出现。",
                    "确认删除", MessageBoxButtons.YesNo, MessageBoxIcon.Warning);
                if (confirmation != DialogResult.Yes) return;
                UseWaitCursor = true;
                operation.Token.ThrowIfCancellationRequested();

                NodeManagementRequest request = new NodeManagementRequest
                {
                    renames = new Dictionary<string, string>(),
                    deletes = selectedIds
                };
                NodeManagementResult result = await Task.Run(delegate
                {
                    return RunNodeManagement(outputPath, request, operation.Token);
                }, operation.Token);
                operation.OutputCommitted = true;
                operation.Token.ThrowIfCancellationRequested();
                SyncManagedNodes(result.nodes);
                RemoveNodeRows(selectedIds);
                passedRowCount = result.nodes == null ? 0 : result.nodes.Length;
                await FinishNodeManagementAsync(
                    "已删除 " + selectedIds.Length + " 个节点", outputPath, operation);
            }
            catch (OperationCanceledException)
            {
                SetStatus(operation != null && operation.OutputCommitted
                    ? "节点管理后续处理已停止；本地结果已保存"
                    : "节点管理已停止；本地结果未修改");
            }
            catch (Exception ex)
            {
                if (operation != null && operation.IsCancellationRequested)
                {
                    SetStatus(operation.OutputCommitted
                        ? "节点管理后续处理已停止；本地结果已保存"
                        : "节点管理已停止；本地结果未修改");
                    return;
                }
                MessageBox.Show(this, ex.Message, "删除失败",
                    MessageBoxButtons.OK, MessageBoxIcon.Error);
                SetStatus("删除失败：" + ex.Message);
            }
            finally
            {
                UseWaitCursor = false;
                if (operation != null) EndTaskOperation(operation);
            }
        }

        private bool HasManagedOutput()
        {
            try
            {
                string path = ResolveOutputPath();
                return !string.IsNullOrWhiteSpace(path) && File.Exists(path);
            }
            catch
            {
                return false;
            }
        }

        private string ResolveManagedOutputPath()
        {
            string path = ResolveOutputPath();
            if (string.IsNullOrWhiteSpace(path) || !File.Exists(path))
                throw new FileNotFoundException("当前本地结果文件不存在，请先完成一次测速。", path);
            return path;
        }

        private NodeManagementResult ListManagedNodes(string outputPath)
        {
            return ListManagedNodes(outputPath, CancellationToken.None);
        }

        private NodeManagementResult ListManagedNodes(string outputPath, CancellationToken cancellationToken)
        {
            return RunNodeManagementUtility(
                "-list-config " + CommandLine.Quote(outputPath), cancellationToken);
        }

        private NodeManagementResult RunNodeManagement(
            string outputPath, NodeManagementRequest request, CancellationToken cancellationToken)
        {
            string directory = Path.GetDirectoryName(outputPath);
            string requestDirectory = Path.Combine(Path.GetTempPath(), "ClashSpeedTestGUI");
            string nonce = Guid.NewGuid().ToString("N");
            string requestPath = Path.Combine(requestDirectory, "manage-" + nonce + ".json");
            string temporaryOutput = Path.Combine(directory,
                "." + Path.GetFileName(outputPath) + ".manage-" + nonce + ".tmp.yaml");
            Directory.CreateDirectory(requestDirectory);
            try
            {
                string json = nodeSerializer.Serialize(request);
                File.WriteAllText(requestPath, json, new UTF8Encoding(false));
                string arguments = "-manage-config " + CommandLine.Quote(requestPath)
                    + " -c " + CommandLine.Quote(outputPath)
                    + " -output " + CommandLine.Quote(temporaryOutput);
                NodeManagementResult result = RunNodeManagementUtility(arguments, cancellationToken);
                cancellationToken.ThrowIfCancellationRequested();
                AtomicFile.Commit(temporaryOutput, outputPath);
                return result;
            }
            finally
            {
                CleanupTempFile(requestPath);
                CleanupTempFile(temporaryOutput);
            }
        }

        private NodeManagementResult RunNodeManagementUtility(
            string arguments, CancellationToken cancellationToken)
        {
            ProcessStartInfo startInfo = new ProcessStartInfo
            {
                FileName = runnerPath,
                Arguments = arguments,
                WorkingDirectory = baseDirectory,
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                StandardOutputEncoding = Encoding.UTF8,
                StandardErrorEncoding = Encoding.UTF8
            };
            using (Process process = new Process { StartInfo = startInfo })
            {
                using (ChildProcessLease lease =
                    ChildProcessLifetime.Start(process, cancellationToken))
                {
                    Task<string> outputTask = process.StandardOutput.ReadToEndAsync();
                    Task<string> errorTask = process.StandardError.ReadToEndAsync();
                    process.WaitForExit();
                    Task.WaitAll(outputTask, errorTask);
                    lease.Complete();
                    cancellationToken.ThrowIfCancellationRequested();
                    string standardOutput = outputTask.Result;
                    string standardError = errorTask.Result;
                    if (process.ExitCode != 0)
                    {
                        string error = string.IsNullOrWhiteSpace(standardError)
                            ? "节点管理内核退出代码：" + process.ExitCode
                            : standardError.Trim();
                        throw new InvalidOperationException(error);
                    }
                    NodeManagementResult result;
                    try
                    {
                        result = nodeSerializer.Deserialize<NodeManagementResult>(standardOutput);
                    }
                    catch (Exception ex)
                    {
                        throw new InvalidOperationException("节点管理内核返回了无法识别的结果。", ex);
                    }
                    if (result == null || result.nodes == null)
                        throw new InvalidOperationException("节点管理内核没有返回完整的节点清单。");
                    return result;
                }
            }
        }

        private void SyncManagedNodes(NodeManifestEvent[] managedNodes)
        {
            foreach (NodeSnapshot node in nodesById.Values) node.Exported = false;
            if (managedNodes == null) return;
            foreach (NodeManifestEvent value in managedNodes)
            {
                if (value == null || string.IsNullOrWhiteSpace(value.id)) continue;
                NodeSnapshot node;
                DataGridViewRow row;
                if (!nodesById.TryGetValue(value.id, out node)
                    || !nodeRows.TryGetValue(value.id, out row)) continue;
                node.Name = value.name ?? "";
                node.Type = value.type ?? "";
                node.ShareUrl = value.share_url ?? "";
                node.ShareError = value.share_error ?? "";
                node.Config = value.config;
                node.Exported = true;
                if (string.IsNullOrWhiteSpace(node.RegionState) || node.RegionState == "—"
                    || node.RegionState == "不查询") node.RegionState = "未查询";
                SetRowValue(row, "节点名称", node.Name);
                SetRowValue(row, "类型", node.Type);
                SetRegionCell(row, node);
            }
            RebuildProtocolFilterOptions();
            ApplyNodeFilters();
        }

        private int ReconcileSpeedExportState(NodeManifestEvent[] exportedNodes)
        {
            HashSet<string> exportedIds = new HashSet<string>(StringComparer.Ordinal);
            foreach (NodeManifestEvent value in exportedNodes ?? new NodeManifestEvent[0])
            {
                if (value == null || string.IsNullOrWhiteSpace(value.id)
                    || !exportedIds.Add(value.id))
                    throw new InvalidOperationException("临时输出包含无效或重复的节点 ID。");
                if (!nodesById.ContainsKey(value.id))
                    throw new InvalidOperationException("临时输出包含测速清单之外的节点 ID：" + value.id);
            }

            int validCount = 0;
            foreach (NodeSnapshot node in nodesById.Values)
            {
                bool valid = exportedIds.Contains(node.Id);
                bool reportedUsable = string.Equals(node.State, "有效", StringComparison.Ordinal);
                if (valid != reportedUsable)
                    throw new InvalidOperationException(
                        "v5 权威结果与临时输出状态不一致，节点 ID：" + node.Id);
                node.Exported = valid;
                node.State = valid ? "有效" : "失败";
                if (!valid)
                    node.RegionState = "不查询";
                else if (!string.Equals(node.RegionState, "成功", StringComparison.Ordinal)
                    && !string.Equals(node.RegionState, "查询失败", StringComparison.Ordinal))
                    node.RegionState = "未查询";
                if (valid) validCount++;
                DataGridViewRow row;
                if (!nodeRows.TryGetValue(node.Id, out row)) continue;
                SetRegionCell(row, node);
                SetNodeStatusCell(row, node);
                ApplyRowStateStyle(row, valid);
            }
            displayedPassedCount = validCount;
            if (userSelectedSort)
            {
                try { ApplyActiveUserSort(); } catch { }
            }
            else
            {
                ApplyDefaultNodeSort();
            }
            ApplyNodeFilters();
            return validCount;
        }

        private void RemoveNodeRows(IEnumerable<string> ids)
        {
            foreach (string id in ids.ToArray())
            {
                DataGridViewRow row;
                if (nodeRows.TryGetValue(id, out row) && row.DataGridView == resultGrid)
                    resultGrid.Rows.Remove(row);
                nodeRows.Remove(id);
                nodesById.Remove(id);
            }
            RebuildProtocolFilterOptions();
            RebuildRegionFilterOptions();
            ApplyNodeFilters();
            UpdateTaskControlState();
        }

        private async Task FinishNodeManagementAsync(
            string action, string outputPath, TaskOperation operation)
        {
            if (operation == null) throw new ArgumentNullException("operation");
            operation.Token.ThrowIfCancellationRequested();
            string detail = action + "；本地结果已原子更新；原订阅未修改";
            if (!gistCheck.Checked)
            {
                SetStatus(detail + "；Gist 未启用");
                MessageBox.Show(this, detail + "。", "节点管理完成",
                    MessageBoxButtons.OK, MessageBoxIcon.Information);
                return;
            }

            try
            {
                string username = gistUsernameText.Text.Trim();
                string token = gistTokenText.Text.Trim();
                if (string.IsNullOrWhiteSpace(username) || string.IsNullOrWhiteSpace(token))
                    throw new InvalidOperationException("Gist 已启用，但 GitHub 用户名或 Token 为空。");
                SetStatus(detail + "；正在更新 Gist…");
                GistUploadResult upload = await Task.Run(delegate
                {
                    return GistClient.CreateOrUpdate(
                        token, username, outputPath, operation.Token);
                }, operation.Token);
                operation.Token.ThrowIfCancellationRequested();
                SetStatus(detail + (upload.FileCreated ? "；Gist 文件链接已创建" : "；Gist 文件已更新"));
                MessageBox.Show(this, detail + (upload.FileCreated
                    ? "。\n\n已按输出文件名创建订阅链接：\n"
                    : "。\n\n同名 Gist 文件已覆盖，订阅链接保持不变：\n") + upload.RawUrl,
                    "节点管理完成", MessageBoxButtons.OK, MessageBoxIcon.Information);
            }
            catch (OperationCanceledException)
            {
                throw;
            }
            catch (Exception ex)
            {
                SetStatus(detail + "；Gist 更新失败");
                MessageBox.Show(this,
                    detail + "。\n\nGist 更新失败：" + ex.Message,
                    "本地已保存，Gist 更新失败", MessageBoxButtons.OK, MessageBoxIcon.Warning);
            }
        }

        private void ApplyDefaultNodeSort()
        {
            if (userSelectedSort || nodeRows.Count == 0) return;
            try
            {
                resultGrid.Sort(new NodeRowComparer("", ListSortDirection.Ascending, true));
                foreach (DataGridViewColumn column in resultGrid.Columns)
                    column.HeaderCell.SortGlyphDirection = SortOrder.None;
            }
            catch
            {
            }
            ReindexVisibleRows();
        }

        private void ResetNodeListFilters()
        {
            applyingNodeListFilters = true;
            try
            {
                statusFilterCombo.SelectedIndex = 0;
                latencyFilterCombo.SelectedIndex = 0;
                protocolFilterCombo.SelectedIndex = 0;
                regionFilterCombo.SelectedIndex = 0;
            }
            finally
            {
                applyingNodeListFilters = false;
            }
            ApplyNodeFilters();
        }

        private void ResetRegionFilterOptions()
        {
            applyingNodeListFilters = true;
            try
            {
                regionFilterCombo.Items.Clear();
                regionFilterCombo.Items.Add("全部");
                regionFilterCombo.SelectedIndex = 0;
                regionFilterCombo.Enabled = false;
            }
            finally
            {
                applyingNodeListFilters = false;
            }
        }

        private void ResetProtocolFilterOptions()
        {
            if (protocolFilterCombo == null) return;
            applyingNodeListFilters = true;
            try
            {
                protocolFilterCombo.Items.Clear();
                protocolFilterCombo.Items.Add("全部");
                protocolFilterCombo.SelectedIndex = 0;
            }
            finally
            {
                applyingNodeListFilters = false;
            }
        }

        private void RebuildProtocolFilterOptions()
        {
            if (protocolFilterCombo == null) return;
            string selected = GetFilterSelection(protocolFilterCombo);
            object[] options = NodeListPresentation.ProtocolFilterOptions(nodesById.Values);
            applyingNodeListFilters = true;
            try
            {
                protocolFilterCombo.Items.Clear();
                protocolFilterCombo.Items.AddRange(options);
                int selectedIndex = -1;
                for (int index = 0; index < protocolFilterCombo.Items.Count; index++)
                {
                    if (string.Equals(Convert.ToString(protocolFilterCombo.Items[index],
                        CultureInfo.InvariantCulture), selected, StringComparison.OrdinalIgnoreCase))
                    {
                        selectedIndex = index;
                        break;
                    }
                }
                protocolFilterCombo.SelectedIndex = selectedIndex >= 0 ? selectedIndex : 0;
            }
            finally
            {
                applyingNodeListFilters = false;
            }
        }

        private void RebuildRegionFilterOptions()
        {
            if (regionFilterCombo == null) return;
            string selected = GetFilterSelection(regionFilterCombo);
            object[] options = NodeListPresentation.RegionFilterOptions(nodesById.Values);
            applyingNodeListFilters = true;
            try
            {
                regionFilterCombo.Items.Clear();
                regionFilterCombo.Items.AddRange(options);
                int selectedIndex = -1;
                for (int index = 0; index < regionFilterCombo.Items.Count; index++)
                {
                    if (string.Equals(Convert.ToString(regionFilterCombo.Items[index],
                        CultureInfo.InvariantCulture), selected, StringComparison.OrdinalIgnoreCase))
                    {
                        selectedIndex = index;
                        break;
                    }
                }
                regionFilterCombo.SelectedIndex = selectedIndex >= 0 ? selectedIndex : 0;
                regionFilterCombo.Enabled = options.Length > 1;
            }
            finally
            {
                applyingNodeListFilters = false;
            }
        }

        private NodeListFilterCriteria CaptureNodeListFilter()
        {
            string latency = GetFilterSelection(latencyFilterCombo);
            double latencyLimit = 0;
            if (latency == "<100ms") latencyLimit = 100;
            else if (latency == "<300ms") latencyLimit = 300;
            else if (latency == "<500ms") latencyLimit = 500;
            else if (latency == "<1000ms") latencyLimit = 1000;

            return new NodeListFilterCriteria
            {
                Status = EmptyWhenAll(GetFilterSelection(statusFilterCombo)),
                MaxLatencyExclusive = latencyLimit,
                Protocol = EmptyWhenAll(GetFilterSelection(protocolFilterCombo)),
                RegionCountryCode = RegionFilterCode(GetFilterSelection(regionFilterCombo))
            };
        }

        private static string RegionFilterCode(string value)
        {
            value = EmptyWhenAll(value);
            if (value.Length < 2) return "";
            string code = value.Substring(0, 2).ToUpperInvariant();
            return code.All(delegate(char character) { return character >= 'A' && character <= 'Z'; })
                ? code : "";
        }

        private static string GetFilterSelection(ComboBox combo)
        {
            return combo == null ? "全部" : Convert.ToString(combo.SelectedItem,
                CultureInfo.InvariantCulture) ?? "全部";
        }

        private static string EmptyWhenAll(string value)
        {
            return string.Equals(value, "全部", StringComparison.Ordinal) ? "" : value;
        }

        private void ApplyNodeFilters()
        {
            if (applyingNodeListFilters || resultGrid == null) return;
            NodeListFilterCriteria criteria = CaptureNodeListFilter();
            bool previousSuppression = suppressStatisticsEvents;
            suppressStatisticsEvents = true;
            resultGrid.SuspendLayout();
            try
            {
                foreach (DataGridViewRow row in resultGrid.Rows)
                {
                    NodeSnapshot node = row.Tag as NodeSnapshot;
                    bool visible = node == null || NodeListFilter.Matches(node, criteria);
                    if (!visible)
                    {
                        if (resultGrid.CurrentRow == row) resultGrid.CurrentCell = null;
                        row.Selected = false;
                    }
                    if (row.Visible != visible) row.Visible = visible;
                }
            }
            finally
            {
                resultGrid.ResumeLayout();
                suppressStatisticsEvents = previousSuppression;
            }
            ReindexVisibleRows();
            UpdateStatistics();
        }

        private void UpdateNodeFilterSummary()
        {
            ReindexVisibleRows();
            UpdateStatistics();
        }

        private void ReindexVisibleRows()
        {
            if (resultGrid == null) return;
            int index = 0;
            foreach (DataGridViewRow row in resultGrid.Rows)
            {
                if (!row.Visible) continue;
                SetRowValue(row, "序号", NodeListPresentation.NextVisibleIndex(ref index));
            }
        }

        private void UpdateStatistics()
        {
            if (statisticsLabel == null || resultGrid == null) return;
            List<NodeSnapshot> nodes = resultGrid.Rows.Cast<DataGridViewRow>()
                .Select(delegate(DataGridViewRow row) { return row.Tag as NodeSnapshot; })
                .Where(delegate(NodeSnapshot node) { return node != null; }).ToList();
            int visible = resultGrid.Rows.Cast<DataGridViewRow>()
                .Count(delegate(DataGridViewRow row) { return row.Visible; });
            int selected = NodeListSelection.GetSelectedVisibleRows(resultGrid).Count;
            int valid = nodes.Count(delegate(NodeSnapshot node)
                { return string.Equals(node.State, "有效", StringComparison.Ordinal); });
            int failed = nodes.Count(delegate(NodeSnapshot node)
                { return string.Equals(node.State, "失败", StringComparison.Ordinal); });
            int waiting = nodes.Count(delegate(NodeSnapshot node)
                {
                    return !string.Equals(node.State, "有效", StringComparison.Ordinal)
                        && !string.Equals(node.State, "失败", StringComparison.Ordinal);
                });
            statisticsLabel.Text = StatusStatistics.Format(
                nodes.Count, visible, selected, valid, failed, waiting);
        }

        private void BuildBasicControls()
        {
            AddLabel(optionsPanel, "配置/订阅/节点：", 15, 18, 110);
            configText = AddTextBox(optionsPanel, 125, 14, 825);
            configText.Name = "ConfigSourceInput";
            configText.TabIndex = 0;
            configText.Multiline = true;
            configText.AcceptsReturn = true;
            configText.ScrollBars = ScrollBars.Vertical;
            configText.WordWrap = false;
            configText.MaxLength = 0;
            configText.Height = 72;
            Button configBrowse = AddButton(optionsPanel, "选择文件", 960, 13, 90);
            configBrowse.Name = "ConfigBrowseButton";
            configBrowse.TabIndex = 1;
            configBrowse.Click += delegate { BrowseConfig(); };
            AddLabel(optionsPanel, "可填订阅/文件/节点；多个输入请每行一个（逗号属于内容）", 125, 92, 700);

            AddLabel(optionsPanel, "延迟上限：", 15, 128, 85);
            latencyNumber = AddNumber(optionsPanel, 100, 123, 110, 0, 600000, 0);
            latencyNumber.Name = "LatencyLimitInput";
            latencyNumber.TabIndex = 3;
            AddLabel(optionsPanel, "ms", 214, 128, 28);
            AddLabel(optionsPanel, "下载下限：", 260, 128, 85);
            downloadSpeedNumber = AddNumber(optionsPanel, 345, 123, 110, 0, 100000, 2);
            downloadSpeedNumber.Name = "DownloadLimitInput";
            downloadSpeedNumber.TabIndex = 4;
            AddLabel(optionsPanel, "MB/s", 460, 128, 50);
            AddLabel(optionsPanel, "0 表示不限制", 525, 128, 130);

            AddLabel(optionsPanel, "输出文件：", 15, 162, 85);
            outputText = AddTextBox(optionsPanel, 100, 158, 850);
            outputText.Name = "OutputPathInput";
            outputText.TabIndex = 5;
            outputText.TextChanged += delegate
            {
                UpdateOutputActionState();
                UpdateRegionActionState();
            };
            Button outputBrowse = AddButton(optionsPanel, "选择位置", 960, 157, 90);
            outputBrowse.Name = "OutputBrowseButton";
            outputBrowse.TabIndex = 6;
            outputBrowse.Click += delegate { BrowseOutput(); };

            renameCheck = new CheckBox
            {
                Name = "RenameNodesCheckBox",
                TabIndex = 7,
                Text = "按真实出口地区重命名节点（例如：🇭🇰 香港 HK-01）",
                Location = new Point(100, 195),
                AutoSize = true
            };
            optionsPanel.Controls.Add(renameCheck);

            gistCheck = new CheckBox
            {
                Name = "GistEnabledCheckBox",
                TabIndex = 8,
                Text = "测速成功后自动创建或更新秘密 GitHub Gist（不公开列出）",
                Location = new Point(100, 226),
                AutoSize = true
            };
            gistCheck.CheckedChanged += delegate { UpdateGistControlState(); };
            optionsPanel.Controls.Add(gistCheck);

            AddLabel(optionsPanel, "GitHub 用户名：", 100, 259, 100);
            gistUsernameText = AddTextBox(optionsPanel, 200, 255, 475);
            gistUsernameText.Name = "GistUsernameInput";
            gistUsernameText.TabIndex = 9;
            gistUsernameText.Anchor = AnchorStyles.Top | AnchorStyles.Left;

            AddLabel(optionsPanel, "Token：", 690, 259, 55);
            gistTokenText = AddTextBox(optionsPanel, 745, 255, 205);
            gistTokenText.Name = "GistTokenInput";
            gistTokenText.TabIndex = 10;
            gistTokenText.UseSystemPasswordChar = true;
            tokenEyeButton = AddButton(optionsPanel, "\uD83D\uDC41", 960, 254, 42);
            tokenEyeButton.Name = "TokenVisibilityButton";
            tokenEyeButton.TabIndex = 11;
            tokenEyeButton.Click += delegate
            {
                gistTokenText.UseSystemPasswordChar = !gistTokenText.UseSystemPasswordChar;
                tokenEyeButton.BackColor = gistTokenText.UseSystemPasswordChar ? SystemColors.Control : Color.LightSteelBlue;
            };
            Button clearTokenButton = AddButton(optionsPanel, "清除输入与凭据", 1008, 254, 117);
            clearTokenButton.Name = "ClearTokenButton";
            clearTokenButton.TabIndex = 12;
            clearTokenButton.Click += delegate
            {
                if (MessageBox.Show(this,
                    "将从界面和 settings.json 中清除订阅/节点输入、GitHub 用户名和 Token。\n\n"
                    + "请先确认 Token 已保存在安全的密码管理器中。",
                    "清除输入与凭据", MessageBoxButtons.OKCancel,
                    MessageBoxIcon.Warning) != DialogResult.OK) return;
                configText.Clear();
                gistUsernameText.Clear();
                gistTokenText.Clear();
                gistCheck.Checked = false;
                try
                {
                    SaveSettingsFromControls();
                    SetStatus("订阅/节点输入与 Gist 凭据已从设置中清除");
                }
                catch (Exception ex)
                {
                    MessageBox.Show(this, ex.Message, "保存设置失败", MessageBoxButtons.OK, MessageBoxIcon.Error);
                }
            };

            Label tokenWarning = AddLabel(optionsPanel,
                "隐私提示：订阅/节点输入和 Token 会明文保存在本机 settings.json；秘密 Gist 的链接持有者仍可访问。",
                100, 289, 900);
            tokenWarning.ForeColor = Color.DarkOrange;

            startButton = AddButton(optionsPanel, "开始测速", 100, 321, 110);
            startButton.Name = "StartSpeedTestButton";
            startButton.TabIndex = 13;
            startButton.BackColor = Color.FromArgb(45, 125, 210);
            startButton.ForeColor = Color.White;
            startButton.FlatStyle = FlatStyle.Flat;
            startButton.Click += async delegate { await StartSpeedTestAsync(); };

            stopButton = AddButton(optionsPanel, "停止", 220, 321, 90);
            stopButton.Name = "StopTaskButton";
            stopButton.TabIndex = 14;
            stopButton.Enabled = false;
            stopButton.Click += delegate { StopSpeedTest(); };

            Button saveSettingsButton = AddButton(optionsPanel, "保存设置", 320, 321, 100);
            saveSettingsButton.Name = "SaveSettingsButton";
            saveSettingsButton.TabIndex = 15;
            saveSettingsButton.Click += delegate
            {
                try
                {
                    SaveSettingsFromControls();
                    SetStatus("设置已保存");
                }
                catch (Exception ex)
                {
                    MessageBox.Show(this, ex.Message, "保存设置失败", MessageBoxButtons.OK, MessageBoxIcon.Error);
                }
            };

            advancedButton = AddButton(optionsPanel, "展开高级设置 ▼", 440, 321, 150);
            advancedButton.Name = "AdvancedSettingsButton";
            advancedButton.TabIndex = 16;
            advancedButton.Click += delegate { ApplyAdvancedState(!advancedGroup.Visible); };

            openOutputButton = AddButton(optionsPanel, "打开结果位置", 600, 321, 110);
            openOutputButton.Name = "OpenOutputButton";
            openOutputButton.TabIndex = 17;
            openOutputButton.Click += delegate { OpenOutputLocation(); };

            copyOutputButton = AddButton(optionsPanel, "复制结果路径", 720, 321, 110);
            copyOutputButton.Name = "CopyOutputPathButton";
            copyOutputButton.TabIndex = 18;
            copyOutputButton.Click += delegate { CopyOutputPath(); };

            AddLabel(optionsPanel, "测速方案：", 845, 323, 75);
            presetCombo = new ComboBox
            {
                Name = "SpeedPresetCombo",
                TabIndex = 19,
                Location = new Point(920, 321),
                Width = 145,
                DropDownStyle = ComboBoxStyle.DropDownList
            };
            presetCombo.Items.AddRange(new object[]
            {
                "快速（仅延迟）",
                "均衡（推荐）",
                "深度（大流量纯下载）",
                "自定义"
            });
            presetCombo.SelectedIndexChanged += delegate { ApplySelectedPreset(); };
            optionsPanel.Controls.Add(presetCombo);

            presetHintLabel = AddLabel(optionsPanel, "", 600, 348, 465);
            presetHintLabel.ForeColor = Color.DimGray;

            AddLabel(optionsPanel, "列表筛选：", 15, 380, 85);
            AddLabel(optionsPanel, "状态", 100, 380, 38);
            statusFilterCombo = AddListFilterCombo(optionsPanel, 138, 375, 86,
                new object[] { "全部", "有效", "失败" });
            statusFilterCombo.Name = "StatusFilter";
            statusFilterCombo.TabIndex = 20;
            AddLabel(optionsPanel, "延迟", 235, 380, 38);
            latencyFilterCombo = AddListFilterCombo(optionsPanel, 273, 375, 102,
                new object[] { "全部", "<100ms", "<300ms", "<500ms", "<1000ms" });
            latencyFilterCombo.Name = "LatencyFilter";
            latencyFilterCombo.TabIndex = 21;
            AddLabel(optionsPanel, "协议", 386, 380, 38);
            protocolFilterCombo = AddListFilterCombo(optionsPanel, 424, 375, 102,
                new object[] { "全部" });
            protocolFilterCombo.Name = "ProtocolFilter";
            protocolFilterCombo.TabIndex = 22;
            AddLabel(optionsPanel, "地区", 537, 380, 38);
            regionFilterCombo = AddListFilterCombo(optionsPanel, 575, 375, 102,
                NodeListPresentation.RegionFilterOptions(null));
            regionFilterCombo.Name = "RegionFilter";
            regionFilterCombo.TabIndex = 23;
            regionFilterCombo.Enabled = false;
            Button resetListFiltersButton = AddButton(optionsPanel, "重置", 690, 375, 66);
            resetListFiltersButton.Name = "ResetFiltersButton";
            resetListFiltersButton.TabIndex = 24;
            resetListFiltersButton.Click += delegate { ResetNodeListFilters(); };
            queryRegionButton = AddButton(optionsPanel, "查询有效节点出口地区", 770, 375, 205);
            queryRegionButton.Name = "QueryRegionsButton";
            queryRegionButton.TabIndex = 25;
            queryRegionButton.Enabled = false;
            queryRegionButton.Click += async delegate
            {
                await QueryRegionsAsync(nodesById.Values.Where(delegate(NodeSnapshot node)
                {
                    return node.Exported && string.Equals(node.State, "有效", StringComparison.Ordinal)
                        && !string.Equals(node.RegionState, "成功", StringComparison.Ordinal);
                }).ToList(), false);
            };

            statusFilterCombo.SelectedIndexChanged += delegate { ApplyNodeFilters(); };
            latencyFilterCombo.SelectedIndexChanged += delegate { ApplyNodeFilters(); };
            protocolFilterCombo.SelectedIndexChanged += delegate { ApplyNodeFilters(); };
            regionFilterCombo.SelectedIndexChanged += delegate { ApplyNodeFilters(); };

            taskConfigurationControls.AddRange(new Control[]
            {
                configText, configBrowse, latencyNumber, downloadSpeedNumber,
                outputText, outputBrowse, renameCheck, gistCheck, gistUsernameText,
                gistTokenText, tokenEyeButton, clearTokenButton, saveSettingsButton, presetCombo
            });
        }

        private GroupBox BuildAdvancedControls()
        {
            GroupBox group = new GroupBox
            {
                Name = "AdvancedSettingsGroup",
                TabIndex = 26,
                Text = "高级设置",
                Location = new Point(15, 411),
                Size = new Size(1130, 205)
            };

            AddLabel(group, "节点正则过滤：", 15, 29, 100);
            filterText = AddTextBox(group, 115, 25, 400);
            filterText.Name = "NodeFilterInput";
            filterText.TabIndex = 0;
            AddLabel(group, "支持正则，例如：HK|香港；留空表示全部", 525, 29, 400);

            AddLabel(group, "测速模式：", 15, 63, 75);
            modeCombo = new ComboBox
            {
                Name = "SpeedModeCombo",
                TabIndex = 1,
                Location = new Point(90, 59),
                Width = 120,
                DropDownStyle = ComboBoxStyle.DropDownList
            };
            modeCombo.Items.AddRange(new object[] { "fast", "download" });
            modeCombo.SelectedIndexChanged += delegate
            {
                UpdateModeControlState();
                MarkPresetCustom();
            };
            group.Controls.Add(modeCombo);

            AddLabel(group, "排除关键词：", 230, 63, 90);
            blockText = AddTextBox(group, 320, 59, 250);
            blockText.Name = "BlockKeywordsInput";
            blockText.TabIndex = 2;
            AddLabel(group, "用 | 分隔", 575, 63, 70);

            AddLabel(group, "User-Agent：", 660, 63, 85);
            userAgentText = AddTextBox(group, 745, 59, 350);
            userAgentText.Name = "UserAgentInput";
            userAgentText.TabIndex = 3;

            AddLabel(group, "测速地址：", 15, 98, 75);
            serverUrlText = AddTextBox(group, 90, 94, 1005);
            serverUrlText.Name = "ServerUrlInput";
            serverUrlText.TabIndex = 4;

            AddLabel(group, "下载量：", 15, 133, 65);
            downloadSizeNumber = AddNumber(group, 80, 128, 75, 1, 10240, 0);
            downloadSizeNumber.Name = "DownloadSizeInput";
            downloadSizeNumber.TabIndex = 5;
            AddLabel(group, "MB", 160, 133, 30);

            AddLabel(group, "探测超时：", 205, 133, 70);
            probeTimeoutNumber = AddNumber(group, 275, 128, 70, 0.1M, 60, 1);
            probeTimeoutNumber.Name = "ProbeTimeoutInput";
            probeTimeoutNumber.TabIndex = 6;
            AddLabel(group, "秒", 350, 133, 30);

            AddLabel(group, "下载超时：", 395, 133, 70);
            timeoutNumber = AddNumber(group, 465, 128, 70, 0.1M, 3600, 1);
            timeoutNumber.Name = "DownloadTimeoutInput";
            timeoutNumber.TabIndex = 7;
            AddLabel(group, "秒", 540, 133, 30);

            AddLabel(group, "延迟并发：", 565, 133, 70);
            concurrentNumber = AddNumber(group, 635, 128, 60, 1, 128, 0);
            concurrentNumber.Name = "NodeConcurrencyInput";
            concurrentNumber.TabIndex = 8;

            AddLabel(group, "HTTP 探测失败率：", 715, 133, 125);
            probeFailureNumber = AddNumber(group, 840, 128, 55, 0, 100, 1);
            probeFailureNumber.Name = "ProbeFailureLimitInput";
            probeFailureNumber.TabIndex = 9;
            AddLabel(group, "%", 900, 133, 20);

            AddLabel(group, "单节点下载连接：", 15, 168, 115);
            transferConcurrentNumber = AddNumber(group, 130, 163, 65, 1, 16, 0);
            transferConcurrentNumber.Name = "TransferConcurrencyInput";
            transferConcurrentNumber.TabIndex = 10;
            AddLabel(group, "仅 download 模式生效；节点间串行下载，当前节点内部允许多连接", 205, 168, 600);

            latencyNumber.ValueChanged += delegate { MarkPresetCustom(); };
            downloadSpeedNumber.ValueChanged += delegate { MarkPresetCustom(); };
            downloadSizeNumber.ValueChanged += delegate { MarkPresetCustom(); };
            probeTimeoutNumber.ValueChanged += delegate { MarkPresetCustom(); };
            timeoutNumber.ValueChanged += delegate { MarkPresetCustom(); };
            concurrentNumber.ValueChanged += delegate { MarkPresetCustom(); };
            transferConcurrentNumber.ValueChanged += delegate { MarkPresetCustom(); };
            probeFailureNumber.ValueChanged += delegate { MarkPresetCustom(); };

            return group;
        }

        private void LoadSettingsToControls()
        {
            configText.Text = settings.ConfigSource;
            filterText.Text = settings.FilterRegex;
            SetNumber(latencyNumber, settings.MaxLatencyMs);
            SetNumber(downloadSpeedNumber, settings.MinDownloadSpeed);
            outputText.Text = string.IsNullOrWhiteSpace(settings.OutputPath) ? "filtered.yaml" : settings.OutputPath;
            renameCheck.Checked = settings.RenameNodes;
            gistCheck.Checked = settings.GistEnabled;
            gistUsernameText.Text = settings.GistUsername;
            gistTokenText.Text = settings.GistToken;

            int modeIndex = modeCombo.Items.IndexOf(settings.SpeedMode);
            modeCombo.SelectedIndex = modeIndex >= 0 ? modeIndex : 1;
            blockText.Text = settings.BlockKeywords;
            serverUrlText.Text = settings.ServerUrl;
            SetNumber(downloadSizeNumber, settings.DownloadSizeMb);
            SetNumber(probeTimeoutNumber, settings.ProbeTimeoutSeconds);
            SetNumber(timeoutNumber, settings.TimeoutSeconds);
            SetNumber(concurrentNumber, settings.Concurrent);
            SetNumber(transferConcurrentNumber, settings.TransferConcurrent);
            SetNumber(probeFailureNumber, settings.MaxHTTPProbeFailure);
            userAgentText.Text = settings.UserAgent;

            SelectMatchingPreset();
            UpdateGistControlState();
            UpdateModeControlState();
            UpdateOutputActionState();
        }

        private void SaveSettingsFromControls()
        {
            AppSettings current = CaptureSettings();
            SettingsStore.Save(current);
            settings = current;
        }

        private AppSettings CaptureSettings()
        {
            return new AppSettings
            {
                ConfigSource = configText.Text.Trim(),
                FilterRegex = filterText.Text.Trim(),
                MaxLatencyMs = latencyNumber.Value,
                MinDownloadSpeed = downloadSpeedNumber.Value,
                OutputPath = outputText.Text.Trim(),
                RenameNodes = renameCheck.Checked,
                GistEnabled = gistCheck.Checked,
                GistUsername = gistUsernameText.Text.Trim(),
                GistAddress = "",
                GistToken = gistTokenText.Text,
                AdvancedExpanded = advancedGroup.Visible,
                SpeedMode = Convert.ToString(modeCombo.SelectedItem, CultureInfo.InvariantCulture),
                BlockKeywords = blockText.Text.Trim(),
                ServerUrl = serverUrlText.Text.Trim(),
                DownloadSizeMb = downloadSizeNumber.Value,
                ProbeTimeoutSeconds = probeTimeoutNumber.Value,
                TimeoutSeconds = timeoutNumber.Value,
                Concurrent = concurrentNumber.Value,
                TransferConcurrent = transferConcurrentNumber.Value,
                MaxHTTPProbeFailure = probeFailureNumber.Value,
                UserAgent = userAgentText.Text.Trim()
            };
        }

        private RunOptions CaptureRunOptions()
        {
            string outputPath = outputText.Text.Trim();
            if (!Path.IsPathRooted(outputPath))
            {
                outputPath = Path.Combine(baseDirectory, outputPath);
            }

            return new RunOptions
            {
                ConfigSource = SubscriptionUrl.NormalizeSources(configText.Text.Trim()),
                FilterRegex = string.IsNullOrWhiteSpace(filterText.Text) ? ".+" : filterText.Text.Trim(),
                MaxLatencyMs = (double)latencyNumber.Value,
                MinDownloadSpeed = (double)downloadSpeedNumber.Value,
                OutputPath = Path.GetFullPath(outputPath),
                RenameNodes = renameCheck.Checked,
                GistEnabled = gistCheck.Checked,
                GistUsername = gistUsernameText.Text.Trim(),
                GistToken = gistTokenText.Text,
                SpeedMode = Convert.ToString(modeCombo.SelectedItem, CultureInfo.InvariantCulture),
                BlockKeywords = blockText.Text.Trim(),
                ServerUrl = serverUrlText.Text.Trim(),
                DownloadSizeBytes = MegabytesToBytes(downloadSizeNumber.Value),
                ProbeTimeoutSeconds = (double)probeTimeoutNumber.Value,
                TimeoutSeconds = (double)timeoutNumber.Value,
                NodeConcurrent = Decimal.ToInt32(concurrentNumber.Value),
                TransferConcurrent = Decimal.ToInt32(transferConcurrentNumber.Value),
                MaxHTTPProbeFailure = (double)probeFailureNumber.Value,
                UserAgent = userAgentText.Text.Trim()
            };
        }

        private TaskOperation BeginTaskOperation(TaskOperationKind kind)
        {
            if (activeOperation != null || allowClose || closeWhenOperationEnds)
                throw new InvalidOperationException("已有任务正在运行或窗口正在关闭。");
            TaskOperation operation = new TaskOperation(Interlocked.Increment(ref nextOperationId), kind);
            activeOperation = operation;
            runInProgress = kind == TaskOperationKind.SpeedTest;
            regionQueryInProgress = kind == TaskOperationKind.RegionQuery;
            managementInProgress = kind == TaskOperationKind.NodeManagement;
            UpdateTaskControlState();
            return operation;
        }

        private bool IsCurrentOperation(TaskOperation operation, bool allowCanceled)
        {
            return operation != null && ReferenceEquals(activeOperation, operation)
                && (allowCanceled || !operation.IsCancellationRequested);
        }

        private void EndTaskOperation(TaskOperation operation)
        {
            if (!ReferenceEquals(activeOperation, operation)) return;
            bool shouldClose = closeWhenOperationEnds && !IsDisposed;
            if (shouldClose)
            {
                closeWhenOperationEnds = false;
                allowClose = true;
            }
            activeOperation = null;
            runInProgress = false;
            regionQueryInProgress = false;
            managementInProgress = false;
            operation.Dispose();
            UpdateTaskControlState();
            if (shouldClose)
            {
                BeginInvoke(new Action(Close));
            }
        }

        private void RequestTaskCancellation(string status)
        {
            TaskOperation operation = activeOperation;
            if (operation == null) return;
            if (operation.Kind == TaskOperationKind.SpeedTest)
            {
                nodeUiTimer.Stop();
                nodeUiUpdates.Clear();
            }
            if (!operation.IsCancellationRequested) operation.Cancel();
            SetStatus(status);
            UpdateTaskControlState();
        }

        private async Task StartSpeedTestAsync()
        {
            if (allowClose || closeWhenOperationEnds
                || runInProgress || managementInProgress || regionQueryInProgress)
            {
                return;
            }

            RunOptions options;
            TaskOperation operation = null;
            try
            {
                options = CaptureRunOptions();
                if (!string.Equals(configText.Text.Trim(), options.ConfigSource, StringComparison.Ordinal))
                {
                    configText.Text = options.ConfigSource;
                }
                ValidateOptions(options);
            }
            catch (Exception ex)
            {
                MessageBox.Show(this, ex.Message, "无法开始测速", MessageBoxButtons.OK, MessageBoxIcon.Warning);
                return;
            }

            if (File.Exists(options.OutputPath))
            {
                DialogResult overwrite = MessageBox.Show(
                    this,
                    "输出文件已存在：\n" + options.OutputPath + "\n\n是否覆盖？",
                    "确认覆盖",
                    MessageBoxButtons.YesNo,
                    MessageBoxIcon.Question);
                if (overwrite != DialogResult.Yes)
                {
                    return;
                }
            }

            try
            {
                Directory.CreateDirectory(Path.GetDirectoryName(options.OutputPath));
                options.CoreOutputPath = Path.Combine(
                    Path.GetDirectoryName(options.OutputPath),
                    "." + Path.GetFileName(options.OutputPath) + ".cstgui-" + Guid.NewGuid().ToString("N") + ".tmp.yaml");
                SaveSettingsFromControls();
            }
            catch (Exception ex)
            {
                MessageBox.Show(this, ex.Message, "准备测速失败", MessageBoxButtons.OK, MessageBoxIcon.Error);
                return;
            }
            try
            {
                operation = BeginTaskOperation(TaskOperationKind.SpeedTest);
                activeOptions = options;
                parsedRowCount = 0;
                passedRowCount = 0;
                manifestRowCount = 0;
                displayedResultCount = 0;
                displayedPassedCount = 0;
                displayedProbeCount = 0;
                displayedDownloadingNode = "";
                userSelectedSort = false;
                activeSortHeader = "";
                ResetRegionFilterOptions();
                ResetProtocolFilterOptions();
                nodesById.Clear();
                nodeRows.Clear();
                nodeUiUpdates.Clear();
                resultGrid.Columns.Clear();
                resultGrid.Rows.Clear();
                currentHeaders = BuildHeaders(options.SpeedMode);
                ConfigureResultColumns(currentHeaders);
                nodeUiTimer.Start();
                SetStatus("正在启动测速内核…");

                RunResult result = await Task.Run(delegate { return RunCore(options, operation); }, operation.Token);
                nodeUiTimer.Stop();
                operation.Token.ThrowIfCancellationRequested();
                await FlushAllPendingNodeUiEventsAsync(operation);
                operation.Token.ThrowIfCancellationRequested();
                ApplyDefaultNodeSort();
                if (!string.IsNullOrWhiteSpace(result.ProtocolError))
                {
                    MarkPendingNodesFailed(result.ProtocolError);
                    MessageBox.Show(this, result.ProtocolError, "内核输出协议错误",
                        MessageBoxButtons.OK, MessageBoxIcon.Error);
                    SetStatus("测速失败：内核输出协议错误");
                    return;
                }

                if (result.ExitCode != 0)
                {
                    string error = string.IsNullOrWhiteSpace(result.ErrorText) ? "内核退出代码：" + result.ExitCode : result.ErrorText;
                    MarkPendingNodesFailed(error);
                    MessageBox.Show(this, error, "测速失败", MessageBoxButtons.OK, MessageBoxIcon.Error);
                    SetStatus("测速失败");
                    return;
                }

                if (result.TotalRows == 0)
                {
                    throw new InvalidOperationException(
                        "输入内容没有加载到任何可测速节点。\n\n" +
                        "请检查节点链接、配置文件或订阅是否有效；使用订阅时，GUI 会自动按 URL 规则添加 flag=meta。");
                }

                if (!File.Exists(options.CoreOutputPath))
                {
                    throw new FileNotFoundException("测速完成，但内核没有生成临时输出文件。", options.CoreOutputPath);
                }

                operation.Token.ThrowIfCancellationRequested();
                NodeManagementResult authoritativeOutput = await Task.Run(delegate
                {
                    return ListManagedNodes(options.CoreOutputPath, operation.Token);
                }, operation.Token);
                operation.Token.ThrowIfCancellationRequested();
                result.PassedRows = ReconcileSpeedExportState(authoritativeOutput.nodes);
                passedRowCount = result.PassedRows;

                if (!OutputPolicy.ShouldCommit(result.TotalRows, result.PassedRows))
                {
                    string preserved = File.Exists(options.OutputPath)
                        ? "\n\n原有输出文件未被覆盖：\n" + options.OutputPath
                        : "\n\n未生成空的输出文件。";
                    SetStatus(CompletionStatus.Format(result.TotalRows, result.PassedRows)
                        + "；Gist 未更新（无有效节点）");
                    MessageBox.Show(this,
                        "已测试 " + result.TotalRows + " 个节点，但没有节点通过当前筛选条件。\n\n" +
                        "可以适当提高延迟上限、降低速度下限后重试。" + preserved,
                        "没有可用节点",
                        MessageBoxButtons.OK,
                        MessageBoxIcon.Warning);
                }
                else
                {
                    operation.Token.ThrowIfCancellationRequested();
                    if (options.RenameNodes)
                    {
                        authoritativeOutput = await ApplyAutomaticExitRegionRenameAsync(
                            authoritativeOutput, options, operation);
                        operation.Token.ThrowIfCancellationRequested();
                        result.PassedRows = ReconcileSpeedExportState(authoritativeOutput.nodes);
                        passedRowCount = result.PassedRows;
                    }
                    operation.Token.ThrowIfCancellationRequested();
                    AtomicFile.Commit(options.CoreOutputPath, options.OutputPath);
                    operation.OutputCommitted = true;
                    UpdateOutputActionState();
                    operation.Token.ThrowIfCancellationRequested();
                    try
                    {
                        NodeManagementResult managed = await Task.Run(delegate
                        {
                            return ListManagedNodes(options.OutputPath, operation.Token);
                        }, operation.Token);
                        operation.Token.ThrowIfCancellationRequested();
                        SyncManagedNodes(managed.nodes);
                    }
                    catch (OperationCanceledException)
                    {
                        throw;
                    }
                    catch
                    {
                        foreach (NodeSnapshot node in nodesById.Values)
                        {
                            node.Exported = string.Equals(node.State, "有效", StringComparison.Ordinal);
                            if (node.Exported && (node.RegionState == "—" || node.RegionState == "不查询"))
                                node.RegionState = "未查询";
                            DataGridViewRow row;
                            if (nodeRows.TryGetValue(node.Id, out row)) SetRegionCell(row, node);
                        }
                    }

                    string speedSummary = CompletionStatus.Format(result.TotalRows, result.PassedRows);
                    if (options.GistEnabled)
                    {
                        operation.Token.ThrowIfCancellationRequested();
                        SetStatus(speedSummary + "；正在创建或更新 Gist…");
                        try
                        {
                            GistUploadResult upload = await Task.Run(delegate
                            {
                                return GistClient.CreateOrUpdate(
                                    options.GistToken, options.GistUsername, options.OutputPath,
                                    operation.Token);
                            }, operation.Token);
                            operation.Token.ThrowIfCancellationRequested();
                            SetStatus(speedSummary + (upload.FileCreated
                                ? "；Gist 文件链接已创建"
                                : "；Gist 文件已更新"));
                            MessageBox.Show(this,
                                speedSummary + (upload.FileCreated
                                    ? "\n\n筛选结果已保存，并按输出文件名创建订阅链接：\n"
                                    : "\n\n筛选结果已保存；同名 Gist 文件已覆盖，订阅链接保持不变：\n")
                                + upload.RawUrl,
                                "完成", MessageBoxButtons.OK, MessageBoxIcon.Information);
                        }
                        catch (OperationCanceledException)
                        {
                            if (operation.IsCancellationRequested) throw;
                            throw new InvalidOperationException("Gist 请求被中止，但当前任务并未请求停止。");
                        }
                        catch (Exception gistException)
                        {
                            SetStatus(speedSummary + "；Gist 更新失败");
                            MessageBox.Show(this,
                                speedSummary + "\n\n本地筛选结果已保存：\n" + options.OutputPath
                                + "\n\nGist 更新失败：" + gistException.Message,
                                "Gist 更新失败", MessageBoxButtons.OK, MessageBoxIcon.Warning);
                        }
                    }
                    else
                    {
                        operation.Token.ThrowIfCancellationRequested();
                        SetStatus(speedSummary + "；Gist 未启用");
                        MessageBox.Show(this,
                            speedSummary + "\n\n筛选结果已保存：\n" + options.OutputPath,
                            "完成", MessageBoxButtons.OK, MessageBoxIcon.Information);
                    }
                }
            }
            catch (OperationCanceledException ex)
            {
                if (operation != null && operation.IsCancellationRequested)
                {
                    MarkPendingNodesFailed("任务已取消");
                    SetStatus(operation.OutputCommitted
                        ? "已停止后续处理；本地结果已保存；Gist 后续请求已取消，如已进入 Gist 上传阶段请检查远端结果"
                        : "测速已停止；本地输出未修改，Gist 未上传");
                }
                else
                {
                    MessageBox.Show(this, ex.Message, "处理被意外中止",
                        MessageBoxButtons.OK, MessageBoxIcon.Error);
                    SetStatus("处理失败：任务被意外中止");
                }
            }
            catch (Exception ex)
            {
                if (operation != null && operation.IsCancellationRequested)
                {
                    MarkPendingNodesFailed("任务已取消");
                    SetStatus(operation.OutputCommitted
                        ? "已停止后续处理；本地结果已保存；Gist 后续请求已取消，如已进入 Gist 上传阶段请检查远端结果"
                        : "测速已停止；本地输出未修改，Gist 未上传");
                    return;
                }
                string preserved = activeOptions != null && File.Exists(activeOptions.OutputPath)
                    ? "\n\n本地输出文件仍保留在：\n" + activeOptions.OutputPath
                    : "";
                MessageBox.Show(this, ex.Message + preserved, "处理失败", MessageBoxButtons.OK, MessageBoxIcon.Error);
                SetStatus("处理失败：" + ex.Message);
            }
            finally
            {
                nodeUiTimer.Stop();
                nodeUiUpdates.Clear();
                if (options != null)
                {
                    CleanupTempFile(options.CoreOutputPath);
                }
                activeOptions = null;
                if (operation != null) EndTaskOperation(operation);
            }
        }

        private RunResult RunCore(RunOptions options, TaskOperation operation)
        {
            string preparedDirectory = null;
            try
            {
                operation.Token.ThrowIfCancellationRequested();
                string preparedSources = PrepareConfigSources(options, out preparedDirectory, operation);
                operation.Token.ThrowIfCancellationRequested();
                ProcessStartInfo startInfo = new ProcessStartInfo
                {
                    FileName = runnerPath,
                    Arguments = BuildArguments(options, preparedSources),
                    WorkingDirectory = baseDirectory,
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    RedirectStandardOutput = true,
                    RedirectStandardError = true,
                    StandardOutputEncoding = Encoding.UTF8,
                    StandardErrorEncoding = Encoding.UTF8
                };

                StringBuilder errors = new StringBuilder();
                object protocolErrorSync = new object();
                string protocolError = "";
                RunnerProtocolValidator validator =
                    new RunnerProtocolValidator(BuildHeaders(options.SpeedMode));
                using (Process process = new Process { StartInfo = startInfo })
                {
                    process.OutputDataReceived += delegate(object sender, DataReceivedEventArgs e)
                    {
                        if (e.Data == null || operation.IsCancellationRequested) return;
                        lock (protocolErrorSync)
                        {
                            if (!string.IsNullOrWhiteSpace(protocolError)) return;
                        }
                        try
                        {
                            operation.Token.ThrowIfCancellationRequested();
                            HandleOutputLine(e.Data, validator, operation);
                        }
                        catch (OperationCanceledException)
                        {
                        }
                        catch (Exception ex)
                        {
                            lock (protocolErrorSync)
                            {
                                if (string.IsNullOrWhiteSpace(protocolError))
                                    protocolError = "内核输出协议错误：" + ex.Message;
                            }
                            ChildProcessLifetime.TryKill(process);
                        }
                    };
                    process.ErrorDataReceived += delegate(object sender, DataReceivedEventArgs e)
                    {
                        if (!string.IsNullOrWhiteSpace(e.Data))
                        {
                            lock (errors)
                            {
                                errors.AppendLine(e.Data);
                            }
                            SetStatusThreadSafe(e.Data, operation);
                        }
                    };

                    using (ChildProcessLease lease =
                        ChildProcessLifetime.Start(process, operation.Token))
                    {
                        process.BeginOutputReadLine();
                        process.BeginErrorReadLine();
                        process.WaitForExit();
                        int exitCode = process.ExitCode;
                        process.WaitForExit();
                        lease.Complete();
                        operation.Token.ThrowIfCancellationRequested();

                        lock (protocolErrorSync)
                        {
                            if (string.IsNullOrWhiteSpace(protocolError) && exitCode == 0)
                            {
                                try
                                {
                                    validator.ValidateCompletion();
                                }
                                catch (Exception ex)
                                {
                                    protocolError = "内核输出协议错误：" + ex.Message;
                                }
                            }
                        }

                        return new RunResult
                        {
                            ExitCode = exitCode,
                            ErrorText = errors.ToString().Trim(),
                            TotalRows = validator.ResultCount,
                            PassedRows = passedRowCount,
                            ProtocolError = protocolError
                        };
                    }
                }
            }
            finally
            {
                CleanupPreparedDirectory(preparedDirectory);
            }
        }

        private string PrepareConfigSources(
            RunOptions options, out string preparedDirectory, TaskOperation operation)
        {
            operation.Token.ThrowIfCancellationRequested();
            string tempRoot = Path.Combine(Path.GetTempPath(), "ClashSpeedTestGUI");
            preparedDirectory = Path.Combine(tempRoot, Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(preparedDirectory);

            List<string> sources = SubscriptionUrl.GetSources(options.ConfigSource);
            List<string> prepared = new List<string>();
            int outputIndex = 0;
            for (int i = 0; i < sources.Count;)
            {
                operation.Token.ThrowIfCancellationRequested();
                string source = sources[i];
                int consumed = 1;
                if (SubscriptionUrl.IsInlineNode(source))
                {
                    List<string> inlineNodes = new List<string>();
                    while (i + consumed - 1 < sources.Count
                        && SubscriptionUrl.IsInlineNode(sources[i + consumed - 1]))
                    {
                        inlineNodes.Add(sources[i + consumed - 1]);
                        consumed++;
                    }
                    consumed--;
                    source = Path.Combine(preparedDirectory,
                        string.Format(CultureInfo.InvariantCulture, "nodes-{0:D3}.txt", outputIndex + 1));
                    File.WriteAllText(source, string.Join(Environment.NewLine, inlineNodes.ToArray()),
                        new UTF8Encoding(false));
                    SetStatusThreadSafe(string.Format(CultureInfo.InvariantCulture,
                        "正在解析批量节点 {0}-{1}/{2}…", i + 1, i + consumed, sources.Count), operation);
                }
                else
                {
                    SetStatusThreadSafe(string.Format(CultureInfo.InvariantCulture,
                        "正在解析输入 {0}/{1}…", i + 1, sources.Count), operation);
                }

                string output = Path.Combine(preparedDirectory,
                    string.Format(CultureInfo.InvariantCulture, "config-{0:D3}.yaml", outputIndex + 1));
                RunSubscriptionParser(source, output, options.UserAgent, operation);
                prepared.Add(output);
                outputIndex++;
                i += consumed;
            }
            operation.Token.ThrowIfCancellationRequested();
            SetStatusThreadSafe("输入解析完成，正在启动测速内核…", operation);
            return string.Join(",", prepared.ToArray());
        }

        private void RunSubscriptionParser(
            string source, string output, string userAgent, TaskOperation operation)
        {
            List<string> arguments = new List<string>();
            AddArgument(arguments, "-input", source);
            AddArgument(arguments, "-output", output);
            if (!string.IsNullOrWhiteSpace(userAgent)) AddArgument(arguments, "-ua", userAgent);
            AddArgument(arguments, "-timeout", "30s");

            ProcessStartInfo startInfo = new ProcessStartInfo
            {
                FileName = parserPath,
                Arguments = string.Join(" ", arguments.ToArray()),
                WorkingDirectory = baseDirectory,
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                StandardOutputEncoding = Encoding.UTF8,
                StandardErrorEncoding = Encoding.UTF8
            };

            using (Process process = new Process { StartInfo = startInfo })
            {
                using (ChildProcessLease lease =
                    ChildProcessLifetime.Start(process, operation.Token))
                {
                    Task<string> outputTask = process.StandardOutput.ReadToEndAsync();
                    Task<string> errorTask = process.StandardError.ReadToEndAsync();
                    process.WaitForExit();
                    Task.WaitAll(outputTask, errorTask);
                    lease.Complete();
                    operation.Token.ThrowIfCancellationRequested();
                    string standardOutput = outputTask.Result;
                    string standardError = errorTask.Result;
                    if (process.ExitCode != 0 || !File.Exists(output))
                    {
                        string error = string.IsNullOrWhiteSpace(standardError)
                            ? "解析程序未生成配置文件"
                            : standardError.Trim();
                        error = error.Replace(source, "[input]");
                        throw new InvalidOperationException("输入解析失败：" + error);
                    }
                    if (!string.IsNullOrWhiteSpace(standardOutput))
                    {
                        SetStatusThreadSafe("订阅解析完成：" + standardOutput.Trim(), operation);
                    }
                }
            }
        }

        private string BuildArguments(RunOptions options, string preparedConfigSources)
        {
            List<string> values = new List<string>();
            AddArgument(values, "-c", preparedConfigSources);
            AddArgument(values, "-f", options.FilterRegex);
            if (!string.IsNullOrWhiteSpace(options.BlockKeywords)) AddArgument(values, "-b", options.BlockKeywords);
            AddArgument(values, "-server-url", options.ServerUrl);
            AddArgument(values, "-speed-mode", options.SpeedMode);
            AddArgument(values, "-download-size", options.DownloadSizeBytes.ToString(CultureInfo.InvariantCulture));
            AddArgument(values, "-probe-timeout", options.ProbeTimeoutSeconds.ToString("0.###", CultureInfo.InvariantCulture) + "s");
            AddArgument(values, "-timeout", options.TimeoutSeconds.ToString("0.###", CultureInfo.InvariantCulture) + "s");
            AddArgument(values, "-concurrent", options.TransferConcurrent.ToString(CultureInfo.InvariantCulture));
            AddArgument(values, "-node-concurrent", options.NodeConcurrent.ToString(CultureInfo.InvariantCulture));
            AddArgument(values, "-output", options.CoreOutputPath);
            AddArgument(values, "-max-latency", options.MaxLatencyMs.ToString("0.###", CultureInfo.InvariantCulture) + "ms");
            AddArgument(values, "-max-probe-failure", options.MaxHTTPProbeFailure.ToString("0.###", CultureInfo.InvariantCulture));
            AddArgument(values, "-min-download-speed", options.MinDownloadSpeed.ToString("0.###", CultureInfo.InvariantCulture));
            if (!string.IsNullOrWhiteSpace(options.UserAgent)) AddArgument(values, "-ua", options.UserAgent);

            return string.Join(" ", values.ToArray());
        }

        private static void AddArgument(List<string> values, string name, string value)
        {
            values.Add(name);
            values.Add(CommandLine.Quote(value));
        }

        private void HandleOutputLine(
            string line, RunnerProtocolValidator validator, TaskOperation operation)
        {
            if (string.IsNullOrWhiteSpace(line))
            {
                return;
            }
            operation.Token.ThrowIfCancellationRequested();
            if (!IsCurrentOperation(operation, false))
                throw new OperationCanceledException("测速任务已结束。");

            if (line.StartsWith("@protocol\t", StringComparison.Ordinal))
            {
                string[] protocolParts = line.Split('\t');
                int version;
                if (protocolParts.Length != 2 || !int.TryParse(protocolParts[1], NumberStyles.None,
                    CultureInfo.InvariantCulture, out version))
                    throw new InvalidOperationException("协议版本行格式错误。");
                validator.AcceptProtocol(version);
                return;
            }

            string[] cells = line.Split('\t');
            if (cells.Length > 0 && string.Equals(cells[0], "序号", StringComparison.Ordinal))
            {
                validator.AcceptHeader(cells);
                return;
            }

            if (line.StartsWith("@nodes\t", StringComparison.Ordinal))
            {
                int total;
                if (cells.Length != 2 || !int.TryParse(cells[1], NumberStyles.None,
                    CultureInfo.InvariantCulture, out total))
                    throw new InvalidOperationException("节点总数行格式错误。");
                validator.AcceptNodeCount(total);
                SetStatusThreadSafe("已加载 " + total + " 个节点，正在准备并发测速…", operation);
                return;
            }
            if (line.StartsWith("@nodejson\t", StringComparison.Ordinal))
            {
                if (cells.Length != 2 || string.IsNullOrWhiteSpace(cells[1]))
                    throw new InvalidOperationException("节点事件行格式错误。");
                HandleNodeJson(cells[1], validator, operation);
                return;
            }
            if (line.StartsWith("@node\t", StringComparison.Ordinal))
            {
                if (cells.Length != 3 || string.IsNullOrWhiteSpace(cells[1])
                    || string.IsNullOrWhiteSpace(cells[2]))
                    throw new InvalidOperationException("兼容节点行格式错误。");
                string name;
                try
                {
                    int padding = (4 - cells[1].Length % 4) % 4;
                    name = Encoding.UTF8.GetString(Convert.FromBase64String(
                        cells[1] + new string('=', padding)));
                }
                catch (Exception ex)
                {
                    throw new InvalidOperationException("兼容节点名称编码无效。", ex);
                }
                validator.AcceptLegacyNodeMirror(name, cells[2]);
                return;
            }
            if (line.StartsWith("@resultjson\t", StringComparison.Ordinal))
            {
                if (cells.Length != 2 || string.IsNullOrWhiteSpace(cells[1]))
                    throw new InvalidOperationException("结果事件行格式错误。");
                HandleResultJson(cells[1], validator, operation);
                return;
            }
            if (line.StartsWith("@progressjson\t", StringComparison.Ordinal))
            {
                if (cells.Length != 2 || string.IsNullOrWhiteSpace(cells[1]))
                    throw new InvalidOperationException("进度事件行格式错误。");
                HandleProgressJson(cells[1], validator, operation);
                return;
            }

            if (line.StartsWith("@", StringComparison.Ordinal))
                throw new InvalidOperationException("未知事件：" + cells[0]);

            if (cells.Length == currentHeaders.Count)
            {
                validator.AcceptLegacyResultMirror(cells);
                return;
            }

            if (validator.IsComplete
                && line.StartsWith("save config file to: ", StringComparison.Ordinal))
                return;

            throw new InvalidOperationException("出现无法识别或顺序错误的输出行。");
        }

        private void HandleNodeJson(
            string encoded, RunnerProtocolValidator validator, TaskOperation operation)
        {
            NodeManifestEvent value;
            try
            {
                value = DecodeNodeEvent<NodeManifestEvent>(encoded);
            }
            catch (Exception ex)
            {
                throw new InvalidOperationException("节点事件不是有效的 Base64 JSON。", ex);
            }
            validator.AcceptNode(value);
            operation.Token.ThrowIfCancellationRequested();
            NodeSnapshot node = new NodeSnapshot
            {
                Id = value.id,
                Name = value.name ?? "",
                Type = value.type ?? "",
                ShareUrl = value.share_url ?? "",
                ShareError = value.share_error ?? "",
                Config = value.config,
                State = "等待探测",
                RegionState = "—"
            };
            nodeUiUpdates.Enqueue(new NodeUiUpdate
            {
                OperationId = operation.Id,
                Kind = NodeUiUpdateKind.Manifest,
                Node = node
            });
        }

        private void HandleResultJson(
            string encoded, RunnerProtocolValidator validator, TaskOperation operation)
        {
            string json;
            try
            {
                json = DecodeNodeEventJson(encoded);
            }
            catch (Exception ex)
            {
                throw new InvalidOperationException("结果事件不是有效的 Base64 UTF-8 数据。", ex);
            }

            NodeResultEvent value;
            try
            {
                Dictionary<string, object> envelope =
                    nodeSerializer.DeserializeObject(json) as Dictionary<string, object>;
                RunnerProtocolValidator.ValidateResultEnvelope(envelope);
                value = nodeSerializer.Deserialize<NodeResultEvent>(json);
            }
            catch (Exception ex)
            {
                throw new InvalidOperationException("结果事件字段无效：" + ex.Message, ex);
            }
            validator.AcceptResult(value);
            operation.Token.ThrowIfCancellationRequested();
            Interlocked.Increment(ref parsedRowCount);
            if (value.usable.Value) Interlocked.Increment(ref passedRowCount);
            nodeUiUpdates.Enqueue(new NodeUiUpdate
            {
                OperationId = operation.Id,
                Kind = NodeUiUpdateKind.Result,
                Result = value
            });
        }

        private void HandleProgressJson(
            string encoded, RunnerProtocolValidator validator, TaskOperation operation)
        {
            string json;
            try
            {
                json = DecodeNodeEventJson(encoded);
            }
            catch (Exception ex)
            {
                throw new InvalidOperationException("进度事件不是有效的 Base64 UTF-8 数据。", ex);
            }

            NodeProgressEvent value;
            try
            {
                Dictionary<string, object> envelope =
                    nodeSerializer.DeserializeObject(json) as Dictionary<string, object>;
                RunnerProtocolValidator.ValidateProgressEnvelope(envelope);
                value = nodeSerializer.Deserialize<NodeProgressEvent>(json);
            }
            catch (Exception ex)
            {
                throw new InvalidOperationException("进度事件字段无效：" + ex.Message, ex);
            }
            validator.AcceptProgress(value);
            operation.Token.ThrowIfCancellationRequested();
            nodeUiUpdates.Enqueue(new NodeUiUpdate
            {
                OperationId = operation.Id,
                Kind = NodeUiUpdateKind.Progress,
                Progress = value
            });
        }

        private T DecodeNodeEvent<T>(string encoded)
        {
            return nodeSerializer.Deserialize<T>(DecodeNodeEventJson(encoded));
        }

        private static string DecodeNodeEventJson(string encoded)
        {
            int padding = (4 - encoded.Length % 4) % 4;
            return StrictEventUtf8.GetString(
                Convert.FromBase64String(encoded + new string('=', padding)));
        }

        private async Task FlushAllPendingNodeUiEventsAsync(TaskOperation operation)
        {
            while (IsCurrentOperation(operation, false) && nodeUiUpdates.Count > 0)
            {
                FlushPendingNodeUiEvents(200);
                if (nodeUiUpdates.Count > 0) await Task.Yield();
            }
        }

        private void FlushPendingNodeUiEvents(int maximum)
        {
            TaskOperation operation = activeOperation;
            if (operation == null || operation.Kind != TaskOperationKind.SpeedTest
                || operation.IsCancellationRequested || IsDisposed || maximum <= 0) return;
            List<NodeUiUpdate> batch = nodeUiUpdates.Drain(operation.Id, maximum);
            if (batch.Count == 0) return;

            bool previousSuppression = suppressStatisticsEvents;
            string sortError = "";
            bool manifestChanged = false;
            suppressStatisticsEvents = true;
            try
            {
                resultGrid.SuspendLayout();
                try
                {
                    foreach (NodeUiUpdate update in batch)
                    {
                        if (update.Kind == NodeUiUpdateKind.Manifest)
                        {
                            AddNodeRowCore(update.Node);
                            manifestChanged = true;
                        }
                        else if (update.Kind == NodeUiUpdateKind.Progress)
                            UpdateNodeProgressCore(update.Progress);
                        else if (update.Kind == NodeUiUpdateKind.Result)
                            UpdateNodeRowCore(update.Result);
                    }
                    if (userSelectedSort)
                    {
                        try
                        {
                            ApplyActiveUserSort();
                        }
                        catch (Exception ex)
                        {
                            sortError = ex.Message;
                        }
                    }
                }
                finally
                {
                    resultGrid.ResumeLayout();
                }

                if (manifestChanged) RebuildProtocolFilterOptions();
                ApplyNodeFilters();
                SetStatus(!string.IsNullOrWhiteSpace(sortError)
                    ? "列表排序暂时失败，将在下一批重试：" + sortError
                    : FormatSpeedProgress());
            }
            finally
            {
                suppressStatisticsEvents = previousSuppression;
            }
        }

        private void AddNodeRowCore(NodeSnapshot node)
        {
            if (node == null || string.IsNullOrWhiteSpace(node.Id)
                || nodesById.ContainsKey(node.Id)) return;
            nodesById[node.Id] = node;
            object[] values = Enumerable.Repeat<object>("等待探测", resultGrid.Columns.Count).ToArray();
            int rowIndex = resultGrid.Rows.Add(values);
            DataGridViewRow row = resultGrid.Rows[rowIndex];
            row.Tag = node;
            nodeRows[node.Id] = row;
            SetRowValue(row, "序号", (manifestRowCount + 1).ToString(CultureInfo.InvariantCulture));
            SetRowValue(row, "节点名称", node.Name);
            SetRowValue(row, "类型", node.Type);
            SetRowValue(row, "HTTP 延迟", NodeListPresentation.MetricText(false, null));
            SetRowValue(row, "下载速度", NodeListPresentation.MetricText(false, null));
            SetRegionCell(row, node);
            SetNodeStatusCell(row, node);
            manifestRowCount++;
        }

        private string FormatSpeedProgress()
        {
            int waitingDownload = nodesById.Values.Count(delegate(NodeSnapshot node)
            {
                return string.Equals(node.State, "等待下载", StringComparison.Ordinal);
            });
            int valid = nodesById.Values.Count(delegate(NodeSnapshot node)
            {
                return string.Equals(node.State, "有效", StringComparison.Ordinal);
            });
            int failed = nodesById.Values.Count(delegate(NodeSnapshot node)
            {
                return string.Equals(node.State, "失败", StringComparison.Ordinal);
            });
            int waiting = Math.Max(0, nodesById.Count - valid - failed);
            string downloading = nodesById.Values
                .Where(delegate(NodeSnapshot node)
                {
                    return string.Equals(node.State, "下载中", StringComparison.Ordinal);
                })
                .Select(delegate(NodeSnapshot node) { return node.Name; })
                .FirstOrDefault() ?? displayedDownloadingNode;
            if (string.IsNullOrWhiteSpace(downloading)) downloading = "—";
            return "已探测 " + displayedProbeCount + "/" + manifestRowCount
                + " | 等待下载 " + waitingDownload
                + " | 正在下载：" + downloading
                + " | 有效 " + valid + " | 失败 " + failed + " | 等待 " + waiting;
        }

        private void MarkPendingNodesFailed(string reason)
        {
            foreach (NodeSnapshot node in nodesById.Values)
            {
                if (string.Equals(node.State, "有效", StringComparison.Ordinal)
                    || string.Equals(node.State, "失败", StringComparison.Ordinal)) continue;
                node.State = "失败";
                node.StatusDetail = string.IsNullOrWhiteSpace(reason) ? "任务未正常完成" : reason;
                node.RegionState = "不查询";
                DataGridViewRow row;
                if (!nodeRows.TryGetValue(node.Id, out row)) continue;
                SetNodeStatusCell(row, node);
                SetRegionCell(row, node);
                ApplyRowStateStyle(row, false);
            }
            displayedDownloadingNode = "";
            UpdateStatistics();
        }

        private void UpdateNodeProgressCore(NodeProgressEvent progress)
        {
            if (progress == null) return;
            NodeSnapshot node;
            DataGridViewRow row;
            if (!nodesById.TryGetValue(progress.id, out node)
                || !nodeRows.TryGetValue(progress.id, out row)) return;
            if (string.Equals(progress.stage, "probe_completed", StringComparison.Ordinal))
            {
                node.ProbeCompleted = true;
                displayedProbeCount++;
                node.State = string.Equals(activeOptions == null ? "" : activeOptions.SpeedMode,
                    "fast", StringComparison.OrdinalIgnoreCase) ? "探测完成" : "等待下载";
            }
            else if (string.Equals(progress.stage, "download_started", StringComparison.Ordinal))
            {
                node.DownloadStarted = true;
                node.State = "下载中";
                displayedDownloadingNode = node.Name;
            }
            SetNodeStatusCell(row, node);
            UpdateStatistics();
        }

        private void UpdateNodeRowCore(NodeResultEvent result)
        {
            if (result == null || result.metrics == null) return;
            string id = result.id;
            string[] values = result.cells;
            NodeSnapshot node;
            DataGridViewRow row;
            if (values == null || !nodesById.TryGetValue(id, out node)
                || !nodeRows.TryGetValue(id, out row)) return;
            for (int i = 1; i < values.Length && i < currentHeaders.Count; i++)
                SetRowValue(row, currentHeaders[i], values[i]);
            int nameIndex = currentHeaders.IndexOf("节点名称");
            int typeIndex = currentHeaders.IndexOf("类型");
            int latencyIndex = currentHeaders.IndexOf("HTTP 延迟");
            if (nameIndex >= 0) node.Name = values[nameIndex];
            if (typeIndex >= 0) node.Type = values[typeIndex];
            int downloadIndex = currentHeaders.IndexOf("下载速度");
            NodeResultProjection.Apply(node, result);
            SetRowValue(row, "HTTP 延迟", node.LatencyMs > 0 ? values[latencyIndex] : "未测试");
            SetTransferCell(row, "下载速度", node.DownloadTested, node.DownloadComplete,
                downloadIndex >= 0 && node.DownloadMbps > 0 ? values[downloadIndex] : null);
            if (node.DownloadStarted) displayedDownloadingNode = "";
            SetRegionCell(row, node);
            SetNodeStatusCell(row, node);
            ApplyRowStateStyle(row, node.State == "有效");
            displayedResultCount++;
            if (node.State == "有效") displayedPassedCount++;
        }

        private static void ApplyRowStateStyle(DataGridViewRow row, bool valid)
        {
            row.DefaultCellStyle.ForeColor = valid ? Color.Black : Color.DimGray;
            row.DefaultCellStyle.BackColor = valid
                ? Color.FromArgb(231, 247, 235)
                : Color.FromArgb(245, 245, 245);
        }

        private static void SetRowValue(DataGridViewRow row, string header, object value)
        {
            if (row == null || row.DataGridView == null) return;
            foreach (DataGridViewColumn column in row.DataGridView.Columns)
            {
                if (column.HeaderText == header)
                {
                    row.Cells[column.Index].Value = value;
                    return;
                }
            }
        }

        private static void SetRegionCell(DataGridViewRow row, NodeSnapshot node)
        {
            if (row == null || row.DataGridView == null) return;
            string value = RegionFormatter.Format(node);
            foreach (DataGridViewColumn column in row.DataGridView.Columns)
            {
                if (column.HeaderText != "出口地区") continue;
                row.Cells[column.Index].Value = RegionFormatter.Ellipsize(value, 18);
                row.Cells[column.Index].ToolTipText =
                    string.Equals(node.RegionState, "查询失败", StringComparison.Ordinal)
                        && !string.IsNullOrWhiteSpace(node.RegionError)
                    ? value + "：" + node.RegionError : value;
                return;
            }
        }

        private static void SetTransferCell(DataGridViewRow row, string header,
            bool tested, bool complete, string value)
        {
            if (row == null || row.DataGridView == null) return;
            foreach (DataGridViewColumn column in row.DataGridView.Columns)
            {
                if (column.HeaderText != header) continue;
                row.Cells[column.Index].Value =
                    NodeListPresentation.TransferMetricText(tested, complete, value);
                row.Cells[column.Index].ToolTipText = tested && !complete
                    ? header + "已启动但未完成计划传输；显示的是已传输部分的采样速度，该节点不会导出。"
                    : tested ? header + "已完成计划传输。" : "未启动" + header + "测试。";
                return;
            }
        }

        private static void SetNodeStatusCell(DataGridViewRow row, NodeSnapshot node)
        {
            if (row == null || row.DataGridView == null || node == null) return;
            foreach (DataGridViewColumn column in row.DataGridView.Columns)
            {
                if (column.HeaderText != "状态") continue;
                row.Cells[column.Index].Value = NodeListPresentation.StatusText(node);
                List<string> incomplete = new List<string>();
                if (node.DownloadTested && !node.DownloadComplete) incomplete.Add("下载");
                row.Cells[column.Index].ToolTipText = incomplete.Count == 0
                    ? (!string.IsNullOrWhiteSpace(node.StatusDetail) ? node.StatusDetail : node.State ?? "")
                    : string.Join("、", incomplete.ToArray())
                        + "传输未完成；内核会标记为不可用（usable=false），节点不会导出。";
                return;
            }
        }

        private static List<string> BuildHeaders(string mode)
        {
            if (string.Equals(mode, "fast", StringComparison.OrdinalIgnoreCase))
            {
                return new List<string> { "序号", "节点名称", "类型", "HTTP 延迟" };
            }
            List<string> headers = new List<string>
            {
                "序号", "节点名称", "类型", "HTTP 延迟", "抖动", "HTTP 探测失败率", "下载速度"
            };
            return headers;
        }

        private void ConfigureResultColumns(List<string> headers)
        {
            resultGrid.Columns.Clear();
            foreach (string header in NodeListPresentation.Headers)
            {
                DataGridViewTextBoxColumn column = new DataGridViewTextBoxColumn
                {
                    HeaderText = header,
                    Name = "column" + resultGrid.Columns.Count,
                    SortMode = DataGridViewColumnSortMode.Programmatic
                };
                if (header == "节点名称") column.FillWeight = 220;
                if (header == "序号")
                {
                    column.FillWeight = 45;
                    column.SortMode = DataGridViewColumnSortMode.NotSortable;
                }
                if (header == "出口地区") column.FillWeight = 130;
                resultGrid.Columns.Add(column);
            }
            UpdateNodeFilterSummary();
        }

        private void ValidateOptions(RunOptions options)
        {
            if (!File.Exists(runnerPath))
            {
                throw new FileNotFoundException(
                    "没有找到 speedtest-runner.exe，请把并发测速执行器与 GUI 放在同一文件夹。",
                    runnerPath);
            }
            if (!File.Exists(parserPath))
            {
                throw new FileNotFoundException(
                    "没有找到 subscription-parser.exe，请把解析助手与 GUI 放在同一文件夹。",
                    parserPath);
            }
            if (string.IsNullOrWhiteSpace(options.ConfigSource))
            {
                throw new InvalidOperationException("请填写配置文件路径、订阅地址或节点链接。");
            }

            List<string> localInputPaths = new List<string>();
            foreach (string rawSource in SubscriptionUrl.GetSources(options.ConfigSource))
            {
                string source = rawSource.Trim();
                if (source.StartsWith("http://", StringComparison.OrdinalIgnoreCase)
                    || source.StartsWith("https://", StringComparison.OrdinalIgnoreCase)
                    || SubscriptionUrl.IsInlineNode(source))
                {
                    continue;
                }
                string localPath = Path.IsPathRooted(source)
                    ? source
                    : Path.Combine(baseDirectory, source);
                localPath = Path.GetFullPath(localPath);
                if (!File.Exists(localPath))
                {
                    throw new FileNotFoundException("配置文件不存在：" + localPath, localPath);
                }
                localInputPaths.Add(localPath);
            }

            if (string.IsNullOrWhiteSpace(options.OutputPath))
            {
                throw new InvalidOperationException("请选择输出文件。");
            }
            if (string.IsNullOrWhiteSpace(options.ServerUrl)
                || (!options.ServerUrl.StartsWith("http://", StringComparison.OrdinalIgnoreCase)
                    && !options.ServerUrl.StartsWith("https://", StringComparison.OrdinalIgnoreCase)))
            {
                throw new InvalidOperationException("测速地址必须是 http:// 或 https:// 地址。");
            }
            OutputPathPolicy.EnsureSafe(options.OutputPath, localInputPaths, new[]
            {
                Application.ExecutablePath,
                parserPath,
                runnerPath
            });
            if (options.GistEnabled)
            {
                if (string.IsNullOrWhiteSpace(options.GistUsername))
                    throw new InvalidOperationException("启用 Gist 后必须填写 GitHub 用户名。");
                if (string.IsNullOrWhiteSpace(options.GistToken))
                    throw new InvalidOperationException("启用 Gist 后必须填写 Token。");
            }
        }

        private static void CleanupTempFile(string path)
        {
            if (string.IsNullOrWhiteSpace(path)) return;
            try
            {
                if (File.Exists(path)) File.Delete(path);
            }
            catch
            {
            }
        }

        private static void CleanupPreparedDirectory(string path)
        {
            if (string.IsNullOrWhiteSpace(path) || !Directory.Exists(path)) return;
            try
            {
                string root = Path.GetFullPath(Path.Combine(Path.GetTempPath(), "ClashSpeedTestGUI"))
                    .TrimEnd(Path.DirectorySeparatorChar);
                string target = Path.GetFullPath(path).TrimEnd(Path.DirectorySeparatorChar);
                if (target.StartsWith(root + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase))
                {
                    Directory.Delete(target, true);
                }
            }
            catch
            {
            }
        }

        private async Task<NodeManagementResult> ApplyAutomaticExitRegionRenameAsync(
            NodeManagementResult authoritativeOutput, RunOptions options, TaskOperation operation)
        {
            NodeManifestEvent[] exported = authoritativeOutput == null
                ? null : authoritativeOutput.nodes;
            List<NodeSnapshot> targets = (exported ?? new NodeManifestEvent[0])
                .Select(delegate(NodeManifestEvent item)
                {
                    NodeSnapshot node;
                    return item != null && nodesById.TryGetValue(item.id, out node) ? node : null;
                })
                .Where(delegate(NodeSnapshot node)
                {
                    return node != null && node.Exported
                        && string.Equals(node.State, "有效", StringComparison.Ordinal);
                })
                .ToList();
            if (targets.Count == 0) return authoritativeOutput;

            string requestPath = Path.Combine(Path.GetTempPath(),
                "ClashSpeedTestGUI-auto-region-" + Guid.NewGuid().ToString("N") + ".json");
            string[] targetIds = targets.Select(delegate(NodeSnapshot node) { return node.Id; }).ToArray();
            Dictionary<string, NodeRegionSnapshot> snapshots = targets.ToDictionary(
                delegate(NodeSnapshot node) { return node.Id; },
                delegate(NodeSnapshot node) { return NodeRegionSnapshot.Capture(node); },
                StringComparer.Ordinal);
            try
            {
                File.WriteAllText(requestPath, nodeSerializer.Serialize(new NodeRegionRequest
                {
                    ids = targetIds
                }), new UTF8Encoding(false));
                regionQueryTotal = targets.Count;
                regionQueryCompleted = 0;
                regionQuerySuccess = 0;
                regionQueryFailed = 0;
                regionQueryPendingIds.Clear();
                foreach (NodeSnapshot node in targets)
                {
                    regionQueryPendingIds.Add(node.Id);
                    node.RegionState = "查询中";
                    node.RegionError = "";
                    DataGridViewRow row;
                    if (nodeRows.TryGetValue(node.Id, out row)) SetRegionCell(row, node);
                }
                SetStatus("测速完成，正在查询有效节点的真实出口地区：0/" + targets.Count);

                RunResult regionResult;
                try
                {
                    regionResult = await Task.Run(delegate
                    {
                        return RunRegionCore(options.CoreOutputPath, requestPath, targetIds, operation);
                    }, operation.Token);
                    operation.Token.ThrowIfCancellationRequested();
                    if (regionResult.ExitCode != 0)
                    {
                        throw new InvalidOperationException(string.IsNullOrWhiteSpace(regionResult.ErrorText)
                            ? "地区查询器退出代码：" + regionResult.ExitCode : regionResult.ErrorText);
                    }
                    ApplyRegionBatch(regionResult.RegionEvents, targetIds);
                }
                catch (OperationCanceledException)
                {
                    RestoreRegionSnapshots(snapshots);
                    throw;
                }
                catch (Exception ex)
                {
                    RestoreRegionSnapshots(snapshots);
                    MessageBox.Show(this,
                        "真实出口地区批次未通过完整性校验，本次不执行任何自动重命名。\n\n"
                        + "节点将按原名称保存。原因：" + ex.Message,
                        "自动重命名已跳过", MessageBoxButtons.OK, MessageBoxIcon.Warning);
                    SetStatus("真实出口地区批次失败；将保存原节点名称");
                    return authoritativeOutput;
                }

                regionQueryCompleted = regionResult.RegionEvents.Count;
                regionQuerySuccess = regionResult.RegionEvents.Count(delegate(NodeRegionEvent value)
                {
                    return value.success;
                });
                regionQueryFailed = regionQueryCompleted - regionQuerySuccess;
                regionQueryPendingIds.Clear();
                string presentationWarning = "";
                try
                {
                    RebuildRegionFilterOptions();
                    if (userSelectedSort) ApplyActiveUserSort();
                }
                catch (Exception presentationError)
                {
                    userSelectedSort = false;
                    activeSortHeader = "";
                    ResetRegionFilterOptions();
                    presentationWarning = "；列表显示已重置：" + presentationError.Message;
                }

                Dictionary<string, string> renames = LabelFormatter.BuildRealExitRenameMap(
                    exported, nodesById);
                if (renames.Count == 0)
                {
                    if (presentationWarning.Length > 0) SetStatus("出口地区查询完成" + presentationWarning);
                    return authoritativeOutput;
                }
                NodeManagementRequest request = new NodeManagementRequest
                {
                    renames = renames,
                    deletes = new string[0]
                };
                return await Task.Run(delegate
                {
                    return RunNodeManagement(options.CoreOutputPath, request, operation.Token);
                }, operation.Token);
            }
            finally
            {
                regionQueryPendingIds.Clear();
                CleanupTempFile(requestPath);
            }
        }

        private async Task QueryRegionsAsync(IEnumerable<NodeSnapshot> requestedNodes, bool refresh)
        {
            if (allowClose || closeWhenOperationEnds
                || runInProgress || managementInProgress || regionQueryInProgress) return;
            List<NodeSnapshot> targets = (requestedNodes ?? Enumerable.Empty<NodeSnapshot>())
                .Where(delegate(NodeSnapshot node)
                {
                    return node != null && node.Exported
                        && string.Equals(node.State, "有效", StringComparison.Ordinal)
                        && (refresh || !string.Equals(node.RegionState, "成功", StringComparison.Ordinal));
                })
                .GroupBy(delegate(NodeSnapshot node) { return node.Id; }, StringComparer.Ordinal)
                .Select(delegate(IGrouping<string, NodeSnapshot> group) { return group.First(); })
                .ToList();
            if (targets.Count == 0)
            {
                SetStatus("没有需要查询的有效节点");
                return;
            }

            string requestPath = Path.Combine(Path.GetTempPath(),
                "ClashSpeedTestGUI-region-" + Guid.NewGuid().ToString("N") + ".json");
            TaskOperation operation = null;
            Dictionary<string, NodeRegionSnapshot> snapshots = targets.ToDictionary(
                delegate(NodeSnapshot node) { return node.Id; },
                delegate(NodeSnapshot node) { return NodeRegionSnapshot.Capture(node); },
                StringComparer.Ordinal);
            bool regionStateChanged = false;
            try
            {
                string outputPath = ResolveManagedOutputPath();
                string[] targetIds = targets.Select(delegate(NodeSnapshot node) { return node.Id; }).ToArray();
                string requestJson = nodeSerializer.Serialize(new NodeRegionRequest
                {
                    ids = targetIds
                });
                File.WriteAllText(requestPath, requestJson, new UTF8Encoding(false));

                operation = BeginTaskOperation(TaskOperationKind.RegionQuery);
                regionQueryTotal = targets.Count;
                regionQueryCompleted = 0;
                regionQuerySuccess = 0;
                regionQueryFailed = 0;
                regionQueryPendingIds.Clear();
                regionStateChanged = true;
                foreach (NodeSnapshot node in targets)
                {
                    regionQueryPendingIds.Add(node.Id);
                    node.RegionState = "查询中";
                    node.RegionError = "";
                    DataGridViewRow row;
                    if (nodeRows.TryGetValue(node.Id, out row)) SetRegionCell(row, node);
                }
                SetStatus(FormatRegionProgress());

                RunResult result = await Task.Run(delegate
                {
                    return RunRegionCore(outputPath, requestPath, targetIds, operation);
                }, operation.Token);
                operation.Token.ThrowIfCancellationRequested();
                if (result.ExitCode != 0)
                {
                    throw new InvalidOperationException(string.IsNullOrWhiteSpace(result.ErrorText)
                        ? "地区查询器退出代码：" + result.ExitCode : result.ErrorText);
                }
                ApplyRegionBatch(result.RegionEvents, targetIds);
                regionQueryCompleted = result.RegionEvents.Count;
                regionQuerySuccess = result.RegionEvents.Count(delegate(NodeRegionEvent value)
                {
                    return value.success;
                });
                regionQueryFailed = regionQueryCompleted - regionQuerySuccess;
                regionQueryPendingIds.Clear();
                string presentationWarning = "";
                try
                {
                    RebuildRegionFilterOptions();
                    if (userSelectedSort) ApplyActiveUserSort();
                }
                catch (Exception presentationError)
                {
                    userSelectedSort = false;
                    activeSortHeader = "";
                    ResetRegionFilterOptions();
                    presentationWarning = "；列表显示已重置：" + presentationError.Message;
                }
                regionStateChanged = false;
                SetStatus("出口地区查询完成：成功 " + regionQuerySuccess
                    + "，失败 " + regionQueryFailed + presentationWarning);
            }
            catch (OperationCanceledException)
            {
                if (regionStateChanged)
                {
                    RestoreRegionSnapshots(snapshots);
                    regionStateChanged = false;
                }
                SetStatus("出口地区查询已停止：本批结果未应用，查询前地区信息已恢复"
                    + "（已处理 " + regionQueryCompleted + "/" + regionQueryTotal + "）");
            }
            catch (Exception ex)
            {
                if (regionStateChanged)
                {
                    RestoreRegionSnapshots(snapshots);
                    regionStateChanged = false;
                }
                if (operation != null && operation.IsCancellationRequested)
                {
                    SetStatus("出口地区查询已停止：本批结果未应用，查询前地区信息已恢复"
                        + "（已处理 " + regionQueryCompleted + "/" + regionQueryTotal + "）");
                }
                else
                {
                    SetStatus("出口地区查询失败：本批结果未应用，查询前地区信息已恢复；"
                        + ex.Message);
                    MessageBox.Show(this, ex.Message
                        + "\r\n\r\n本批地区结果未应用，查询前的地区信息已恢复。", "出口地区查询失败",
                        MessageBoxButtons.OK, MessageBoxIcon.Warning);
                }
            }
            finally
            {
                if (regionStateChanged) RestoreRegionSnapshots(snapshots);
                CleanupTempFile(requestPath);
                regionQueryPendingIds.Clear();
                if (operation != null) EndTaskOperation(operation);
                ApplyNodeFilters();
            }
        }

        private RunResult RunRegionCore(
            string outputPath, string requestPath, IEnumerable<string> expectedIds,
            TaskOperation operation)
        {
            RegionProtocolValidator validator = new RegionProtocolValidator(expectedIds);
            List<NodeRegionEvent> events = new List<NodeRegionEvent>();
            ProcessStartInfo startInfo = new ProcessStartInfo
            {
                FileName = runnerPath,
                Arguments = "-c " + CommandLine.Quote(outputPath)
                    + " -region-query " + CommandLine.Quote(requestPath),
                WorkingDirectory = baseDirectory,
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                StandardOutputEncoding = Encoding.UTF8,
                StandardErrorEncoding = Encoding.UTF8
            };
            using (Process process = new Process { StartInfo = startInfo })
            {
                try
                {
                    using (ChildProcessLease lease =
                        ChildProcessLifetime.Start(process, operation.Token))
                    {
                        Task<string> errorTask = process.StandardError.ReadToEndAsync();
                        string line;
                        while ((line = process.StandardOutput.ReadLine()) != null)
                        {
                            operation.Token.ThrowIfCancellationRequested();
                            NodeRegionEvent value = HandleRegionProtocolLine(line, validator);
                            if (value == null) continue;
                            events.Add(value);
                            Invoke(new Action(delegate
                            {
                                if (IsCurrentOperation(operation, false))
                                    ReportRegionEventProgress(value);
                            }));
                        }
                        process.WaitForExit();
                        lease.Complete();
                        operation.Token.ThrowIfCancellationRequested();
                        int exitCode = process.ExitCode;
                        string errorText = errorTask.Result.Trim();
                        if (exitCode == 0)
                        {
                            try
                            {
                                validator.ValidateCompletion();
                            }
                            catch (Exception ex)
                            {
                                throw new InvalidOperationException(
                                    "地区事件协议错误：" + ex.Message, ex);
                            }
                        }
                        return new RunResult
                        {
                            ExitCode = exitCode,
                            ErrorText = errorText,
                            TotalRows = validator.ResultCount,
                            RegionEvents = events
                        };
                    }
                }
                catch
                {
                    ChildProcessLifetime.Terminate(process, 2000);
                    throw;
                }
            }
        }

        private NodeRegionEvent HandleRegionProtocolLine(
            string line, RegionProtocolValidator validator)
        {
            return RegionProtocolLineParser.AcceptLine(line, validator, nodeSerializer);
        }

        private void ReportRegionEventProgress(NodeRegionEvent value)
        {
            if (value == null || !regionQueryPendingIds.Remove(value.node_id))
                throw new InvalidOperationException("地区查询进度与协议结果不同步。");
            regionQueryCompleted++;
            if (value.success) regionQuerySuccess++;
            else regionQueryFailed++;
            SetStatus(FormatRegionProgress());
        }

        private void ApplyRegionBatch(
            IList<NodeRegionEvent> events, IEnumerable<string> expectedIds)
        {
            if (events == null) throw new InvalidOperationException("地区结果批次为空。");
            string[] requested = (expectedIds ?? Enumerable.Empty<string>()).ToArray();
            HashSet<string> expected = new HashSet<string>(requested, StringComparer.Ordinal);
            if (expected.Count != requested.Length || events.Count != expected.Count)
                throw new InvalidOperationException("地区结果批次数量与请求不一致。");

            HashSet<string> seen = new HashSet<string>(StringComparer.Ordinal);
            List<Tuple<NodeSnapshot, NodeRegionEvent>> staged =
                new List<Tuple<NodeSnapshot, NodeRegionEvent>>(events.Count);
            foreach (NodeRegionEvent value in events)
            {
                NodeSnapshot node;
                if (value == null || !expected.Contains(value.node_id)
                    || !seen.Add(value.node_id)
                    || !nodesById.TryGetValue(value.node_id, out node))
                    throw new InvalidOperationException("地区结果批次包含未知、重复或已移除的节点。");
                staged.Add(Tuple.Create(node, value));
            }
            if (seen.Count != expected.Count)
                throw new InvalidOperationException("地区结果批次不完整。");

            foreach (Tuple<NodeSnapshot, NodeRegionEvent> item in staged)
                RegionEventProjection.Apply(item.Item1, item.Item2);
            foreach (Tuple<NodeSnapshot, NodeRegionEvent> item in staged)
            {
                DataGridViewRow row;
                if (nodeRows.TryGetValue(item.Item1.Id, out row)) SetRegionCell(row, item.Item1);
            }
        }

        private void RestoreRegionSnapshots(
            IDictionary<string, NodeRegionSnapshot> snapshots)
        {
            foreach (KeyValuePair<string, NodeRegionSnapshot> item in
                snapshots ?? new Dictionary<string, NodeRegionSnapshot>())
            {
                NodeSnapshot node;
                DataGridViewRow row;
                if (!nodesById.TryGetValue(item.Key, out node) || item.Value == null) continue;
                item.Value.Restore(node);
                if (nodeRows.TryGetValue(item.Key, out row)) SetRegionCell(row, node);
            }
            if (!userSelectedSort) return;
            try
            {
                ApplyActiveUserSort();
            }
            catch
            {
                userSelectedSort = false;
                activeSortHeader = "";
                foreach (DataGridViewColumn column in resultGrid.Columns)
                    column.HeaderCell.SortGlyphDirection = SortOrder.None;
            }
        }

        private string FormatRegionProgress()
        {
            return "正在查询出口地区 " + regionQueryCompleted + "/" + regionQueryTotal
                + "，成功 " + regionQuerySuccess + "，失败 " + regionQueryFailed;
        }

        private void StopSpeedTest()
        {
            RequestTaskCancellation(regionQueryInProgress
                ? "正在停止出口地区查询…"
                : managementInProgress
                    ? "正在停止节点管理并清理临时文件…"
                    : "正在停止测速并清理临时文件…");
        }

        private void UpdateTaskControlState()
        {
            bool busy = activeOperation != null || managementInProgress;
            bool closing = allowClose || closeWhenOperationEnds;
            bool inputsEnabled = TaskControlPolicy.InputsEnabled(busy || closing);
            foreach (Control control in taskConfigurationControls)
                control.Enabled = inputsEnabled;
            startButton.Enabled = !busy && !closing
                && File.Exists(runnerPath) && File.Exists(parserPath);
            stopButton.Enabled = TaskControlPolicy.StopEnabled(activeOperation);
            UpdateRegionActionState();
            UseWaitCursor = managementInProgress;
            if (!busy && !closing)
            {
                UpdateGistControlState();
                UpdateModeControlState();
                UpdateOutputActionState();
            }
        }

        private void UpdateRegionActionState()
        {
            if (queryRegionButton == null) return;
            bool busy = activeOperation != null || managementInProgress;
            bool closing = allowClose || closeWhenOperationEnds;
            queryRegionButton.Enabled = !busy && !closing && HasManagedOutput()
                && nodesById.Values.Any(delegate(NodeSnapshot node)
                {
                    return node.Exported && string.Equals(node.State, "有效", StringComparison.Ordinal)
                        && !string.Equals(node.RegionState, "成功", StringComparison.Ordinal);
                });
        }

        private void ApplyAdvancedState(bool expanded)
        {
            advancedGroup.Visible = expanded;
            advancedButton.Text = expanded ? "收起高级设置 ▲" : "展开高级设置 ▼";
            UpdateOptionsPanelLayout();
        }

        private void UpdateOptionsPanelLayout()
        {
            if (optionsPanel == null || statusStrip == null) return;
            bool expanded = advancedGroup != null && advancedGroup.Visible;
            int basicHeight = ScaleLogical(BasicOptionsHeight);
            int expandedHeight = ScaleLogical(ExpandedOptionsContentHeight);
            int desiredHeight = expanded ? expandedHeight : basicHeight;
            optionsPanel.Height = OptionsLayoutPolicy.PanelHeight(
                ClientSize.Height, statusStrip.Height, desiredHeight,
                basicHeight, ScaleLogical(MinimumResultGridHeight));
            optionsPanel.AutoScrollMinSize = new Size(
                ScaleLogical(OptionsContentWidth), expanded ? expandedHeight : basicHeight);
        }

        private int ScaleLogical(int value)
        {
            int dpi = DeviceDpi > 0 ? DeviceDpi : 96;
            return (int)Math.Round(value * dpi / 96D, MidpointRounding.AwayFromZero);
        }

        private void ApplySelectedPreset()
        {
            if (applyingPreset || presetCombo == null) return;
            SpeedPreset preset = SpeedPresets.Get(presetCombo.SelectedIndex);
            if (preset == null)
            {
                UpdatePresetHint();
                return;
            }

            applyingPreset = true;
            try
            {
                modeCombo.SelectedItem = preset.Mode;
                SetNumber(latencyNumber, preset.MaxLatencyMs);
                SetNumber(downloadSpeedNumber, preset.MinDownloadSpeed);
                SetNumber(downloadSizeNumber, preset.DownloadSizeMb);
                SetNumber(probeTimeoutNumber, preset.ProbeTimeoutSeconds);
                SetNumber(timeoutNumber, preset.TimeoutSeconds);
                SetNumber(concurrentNumber, preset.NodeConcurrent);
                SetNumber(transferConcurrentNumber, preset.TransferConcurrent);
                SetNumber(probeFailureNumber, preset.MaxHTTPProbeFailure);
                UpdateModeControlState();
            }
            finally
            {
                applyingPreset = false;
            }
            UpdatePresetHint();
            SetStatus("已应用测速方案：" + Convert.ToString(presetCombo.SelectedItem, CultureInfo.InvariantCulture));
        }

        private void SelectMatchingPreset()
        {
            applyingPreset = true;
            try
            {
                int selected = 3;
                for (int i = 0; i < 3; i++)
                {
                    SpeedPreset preset = SpeedPresets.Get(i);
                    if (PresetMatchesCurrentValues(preset))
                    {
                        selected = i;
                        break;
                    }
                }
                presetCombo.SelectedIndex = selected;
            }
            finally
            {
                applyingPreset = false;
            }
            UpdatePresetHint();
        }

        private bool PresetMatchesCurrentValues(SpeedPreset preset)
        {
            return preset != null
                && string.Equals(Convert.ToString(modeCombo.SelectedItem, CultureInfo.InvariantCulture),
                    preset.Mode, StringComparison.OrdinalIgnoreCase)
                && latencyNumber.Value == preset.MaxLatencyMs
                && downloadSpeedNumber.Value == preset.MinDownloadSpeed
                && downloadSizeNumber.Value == preset.DownloadSizeMb
                && probeTimeoutNumber.Value == preset.ProbeTimeoutSeconds
                && timeoutNumber.Value == preset.TimeoutSeconds
                && concurrentNumber.Value == preset.NodeConcurrent
                && transferConcurrentNumber.Value == preset.TransferConcurrent
                && probeFailureNumber.Value == preset.MaxHTTPProbeFailure;
        }

        private void MarkPresetCustom()
        {
            if (applyingPreset || presetCombo == null || presetCombo.SelectedIndex < 0) return;
            if (presetCombo.SelectedIndex != 3)
            {
                applyingPreset = true;
                presetCombo.SelectedIndex = 3;
                applyingPreset = false;
            }
            UpdatePresetHint();
        }

        private void UpdatePresetHint()
        {
            if (presetHintLabel == null || presetCombo == null) return;
            SpeedPreset preset = SpeedPresets.Get(presetCombo.SelectedIndex);
            presetHintLabel.Text = preset == null
                ? "已手动调整参数；实际流量取决于节点数量和测试模式。"
                : preset.Hint;
        }

        private string ResolveOutputPath()
        {
            string value = outputText == null ? "" : outputText.Text.Trim();
            if (string.IsNullOrWhiteSpace(value)) return "";
            if (!Path.IsPathRooted(value)) value = Path.Combine(baseDirectory, value);
            return Path.GetFullPath(value);
        }

        private void UpdateOutputActionState()
        {
            if (openOutputButton == null || copyOutputButton == null) return;
            bool available;
            try
            {
                available = !string.IsNullOrWhiteSpace(ResolveOutputPath());
            }
            catch
            {
                available = false;
            }
            openOutputButton.Enabled = available;
            copyOutputButton.Enabled = available;
        }

        private void OpenOutputLocation()
        {
            try
            {
                string path = ResolveOutputPath();
                if (string.IsNullOrWhiteSpace(path)) throw new InvalidOperationException("请先填写输出文件路径。");
                string directory = Path.GetDirectoryName(path);
                if (!Directory.Exists(directory)) Directory.CreateDirectory(directory);
                string arguments = File.Exists(path)
                    ? "/select," + CommandLine.Quote(path)
                    : CommandLine.Quote(directory);
                Process.Start("explorer.exe", arguments);
            }
            catch (Exception ex)
            {
                MessageBox.Show(this, ex.Message, "无法打开结果位置", MessageBoxButtons.OK, MessageBoxIcon.Error);
            }
        }

        private void CopyOutputPath()
        {
            try
            {
                string path = ResolveOutputPath();
                if (string.IsNullOrWhiteSpace(path)) throw new InvalidOperationException("请先填写输出文件路径。");
                Clipboard.SetText(path);
                SetStatus("结果路径已复制");
            }
            catch (Exception ex)
            {
                MessageBox.Show(this, ex.Message, "无法复制结果路径", MessageBoxButtons.OK, MessageBoxIcon.Error);
            }
        }

        private void UpdateGistControlState()
        {
            bool enabled = gistCheck.Checked && activeOperation == null && !managementInProgress;
            gistUsernameText.Enabled = enabled;
            gistTokenText.Enabled = enabled;
            tokenEyeButton.Enabled = enabled;
        }

        private void UpdateModeControlState()
        {
            bool editable = activeOperation == null && !managementInProgress;
            string mode = Convert.ToString(modeCombo.SelectedItem, CultureInfo.InvariantCulture);
            bool fast = string.Equals(mode, "fast", StringComparison.OrdinalIgnoreCase);
            probeTimeoutNumber.Enabled = editable;
            downloadSizeNumber.Enabled = editable && !fast;
            downloadSpeedNumber.Enabled = editable && !fast;
            transferConcurrentNumber.Enabled = editable && !fast;
        }

        private void BrowseConfig()
        {
            using (OpenFileDialog dialog = new OpenFileDialog())
            {
                dialog.Filter = "Clash 配置 (*.yaml;*.yml)|*.yaml;*.yml|所有文件 (*.*)|*.*";
                dialog.CheckFileExists = true;
                if (dialog.ShowDialog(this) == DialogResult.OK)
                {
                    configText.Text = dialog.FileName;
                }
            }
        }

        private void BrowseOutput()
        {
            using (SaveFileDialog dialog = new SaveFileDialog())
            {
                dialog.Filter = "YAML 配置 (*.yaml)|*.yaml|YML 配置 (*.yml)|*.yml|所有文件 (*.*)|*.*";
                dialog.FileName = Path.GetFileName(outputText.Text);
                string current = outputText.Text;
                if (!string.IsNullOrWhiteSpace(current))
                {
                    try
                    {
                        string resolved = Path.IsPathRooted(current) ? current : Path.Combine(baseDirectory, current);
                        dialog.InitialDirectory = Path.GetDirectoryName(Path.GetFullPath(resolved));
                    }
                    catch
                    {
                    }
                }
                if (dialog.ShowDialog(this) == DialogResult.OK)
                {
                    outputText.Text = dialog.FileName;
                }
            }
        }

        private void OnShown(object sender, EventArgs e)
        {
            ConstrainWindowToWorkingArea();
            UpdateOptionsPanelLayout();
            if (!File.Exists(runnerPath))
            {
                MessageBox.Show(this,
                    "没有找到 speedtest-runner.exe。\n请把并发测速执行器与 GUI 放在同一文件夹。",
                    "缺少并发测速执行器",
                    MessageBoxButtons.OK,
                    MessageBoxIcon.Error);
                startButton.Enabled = false;
            }
            else if (!File.Exists(parserPath))
            {
                MessageBox.Show(this,
                    "没有找到 subscription-parser.exe。\n请把解析助手与 GUI 放在同一文件夹。",
                    "缺少订阅解析助手",
                    MessageBoxButtons.OK,
                    MessageBoxIcon.Error);
                startButton.Enabled = false;
            }
        }

        private void ConstrainWindowToWorkingArea()
        {
            Rectangle workingArea = Screen.FromControl(this).WorkingArea;
            int width = Math.Min(Width, workingArea.Width);
            int height = Math.Min(Height, workingArea.Height);
            int x = Math.Max(workingArea.Left,
                Math.Min(Left, workingArea.Right - width));
            int y = Math.Max(workingArea.Top,
                Math.Min(Top, workingArea.Bottom - height));
            Bounds = new Rectangle(x, y, width, height);
        }

        private void OnFormClosing(object sender, FormClosingEventArgs e)
        {
            if (allowClose)
            {
                try { SaveSettingsFromControls(); } catch { }
                return;
            }
            if (activeOperation != null)
            {
                if (closeWhenOperationEnds)
                {
                    e.Cancel = true;
                    return;
                }
                DialogResult result = MessageBox.Show(this,
                    activeOperation.Kind == TaskOperationKind.RegionQuery
                        ? "出口地区查询仍在进行，关闭窗口会停止查询。是否关闭？"
                        : activeOperation.Kind == TaskOperationKind.NodeManagement
                            ? "节点管理仍在进行，关闭窗口会停止后续处理。已原子保存的本地结果不会回滚。是否关闭？"
                            : "测速仍在进行，关闭窗口会停止测速。是否关闭？",
                    "确认关闭",
                    MessageBoxButtons.YesNo,
                    MessageBoxIcon.Warning);
                if (result != DialogResult.Yes)
                {
                    e.Cancel = true;
                    return;
                }
                e.Cancel = true;
                closeWhenOperationEnds = true;
                RequestTaskCancellation("正在停止任务并清理临时文件，完成后将关闭窗口…");
                return;
            }

            try
            {
                SaveSettingsFromControls();
            }
            catch
            {
            }
        }

        protected override void Dispose(bool disposing)
        {
            if (disposing)
            {
                nodeUiTimer.Stop();
                nodeUiTimer.Dispose();
                nodeUiUpdates.Clear();
                if (activeOperation != null) activeOperation.Cancel();
            }
            base.Dispose(disposing);
        }

        private void SetStatus(string text)
        {
            statusLabel.Text = text;
        }

        private void SetStatusThreadSafe(string text, TaskOperation operation)
        {
            if (IsDisposed || operation == null || operation.IsCancellationRequested) return;
            try
            {
                BeginInvoke(new Action(delegate
                {
                    if (IsCurrentOperation(operation, false)) SetStatus(text);
                }));
            }
            catch
            {
            }
        }

        private static int MegabytesToBytes(decimal value)
        {
            decimal bytes = value * 1024M * 1024M;
            if (bytes > int.MaxValue) return int.MaxValue;
            return Decimal.ToInt32(bytes);
        }

        private static void SetNumber(NumericUpDown control, decimal value)
        {
            if (value < control.Minimum) value = control.Minimum;
            if (value > control.Maximum) value = control.Maximum;
            control.Value = value;
        }

        private static Label AddLabel(Control parent, string text, int x, int y, int width)
        {
            Label label = new Label
            {
                Text = text,
                Location = new Point(x, y),
                Size = new Size(width, 23),
                TextAlign = ContentAlignment.MiddleLeft
            };
            parent.Controls.Add(label);
            return label;
        }

        private static TextBox AddTextBox(Control parent, int x, int y, int width)
        {
            TextBox textBox = new TextBox
            {
                Location = new Point(x, y),
                Size = new Size(width, 25)
            };
            parent.Controls.Add(textBox);
            return textBox;
        }

        private static ComboBox AddListFilterCombo(Control parent, int x, int y, int width,
            object[] items)
        {
            ComboBox combo = new ComboBox
            {
                Location = new Point(x, y),
                Size = new Size(width, 25),
                DropDownStyle = ComboBoxStyle.DropDownList
            };
            combo.Items.AddRange(items);
            if (combo.Items.Count > 0) combo.SelectedIndex = 0;
            parent.Controls.Add(combo);
            return combo;
        }

        private static Button AddButton(Control parent, string text, int x, int y, int width)
        {
            Button button = new Button
            {
                Text = text,
                Location = new Point(x, y),
                Size = new Size(width, 27)
            };
            parent.Controls.Add(button);
            return button;
        }

        private static NumericUpDown AddNumber(Control parent, int x, int y, int width,
            decimal minimum, decimal maximum, int decimalPlaces)
        {
            NumericUpDown number = new NumericUpDown
            {
                Location = new Point(x, y),
                Size = new Size(width, 25),
                Minimum = minimum,
                Maximum = maximum,
                DecimalPlaces = decimalPlaces,
                ThousandsSeparator = true
            };
            parent.Controls.Add(number);
            return number;
        }
    }

    internal static class AtomicFile
    {
        public static void Commit(string temporaryPath, string destinationPath)
        {
            if (string.IsNullOrWhiteSpace(temporaryPath) || !File.Exists(temporaryPath))
                throw new FileNotFoundException("临时输出文件不存在。", temporaryPath);
            if (string.IsNullOrWhiteSpace(destinationPath))
                throw new ArgumentException("目标输出路径不能为空。", "destinationPath");

            if (File.Exists(destinationPath))
            {
                File.Replace(temporaryPath, destinationPath, null, true);
            }
            else
            {
                File.Move(temporaryPath, destinationPath);
            }
        }
    }

    internal static class OutputPolicy
    {
        public static bool ShouldCommit(int totalRows, int passedRows)
        {
            return totalRows > 0 && passedRows > 0;
        }
    }

    internal static class CompletionStatus
    {
        public static string Format(int totalRows, int passedRows)
        {
            int failedRows = Math.Max(0, totalRows - passedRows);
            return "测速完成：" + totalRows + "/" + totalRows
                + "，有效 " + passedRows + " 个，失败 " + failedRows + " 个";
        }
    }

    internal sealed class RunResult
    {
        public int ExitCode;
        public string ErrorText;
        public int TotalRows;
        public int PassedRows;
        public string ProtocolError;
        public List<NodeRegionEvent> RegionEvents;
    }

    internal static class SubscriptionUrl
    {
        private static readonly HashSet<string> InlineNodeSchemes =
            new HashSet<string>(StringComparer.OrdinalIgnoreCase)
            {
                "ss", "ssr", "vmess", "vless", "trojan",
                "hysteria", "hysteria2", "hy2", "tuic", "anytls",
                "socks", "socks5", "socks5h"
            };

        public static string NormalizeSources(string sources)
        {
            if (string.IsNullOrWhiteSpace(sources)) return sources;
            List<string> parts = GetSources(sources);
            for (int i = 0; i < parts.Count; i++)
            {
                parts[i] = NormalizeOne(parts[i].Trim());
            }
            return string.Join(Environment.NewLine, parts.ToArray());
        }

        public static List<string> GetSources(string sources)
        {
            List<string> result = new List<string>();
            if (string.IsNullOrWhiteSpace(sources)) return result;

            string normalized = sources.Replace("\r\n", "\n").Replace('\r', '\n');
            foreach (string rawLine in normalized.Split('\n'))
            {
                string line = rawLine.Trim();
                if (line.Length == 0) continue;
                // Every non-empty line is one source. Commas are valid in URLs,
                // Windows file names and node query values, so never split them.
                result.Add(line);
            }
            return result;
        }

        public static bool IsInlineNode(string source)
        {
            if (string.IsNullOrWhiteSpace(source)) return false;
            int schemeEnd = source.IndexOf("://", StringComparison.Ordinal);
            if (schemeEnd <= 0) return false;
            return InlineNodeSchemes.Contains(source.Substring(0, schemeEnd));
        }

        private static string NormalizeOne(string source)
        {
            if (!source.StartsWith("http://", StringComparison.OrdinalIgnoreCase)
                && !source.StartsWith("https://", StringComparison.OrdinalIgnoreCase))
            {
                return source;
            }

            string fragment = "";
            int fragmentIndex = source.IndexOf('#');
            if (fragmentIndex >= 0)
            {
                fragment = source.Substring(fragmentIndex);
                source = source.Substring(0, fragmentIndex);
            }

            if (source.IndexOf('?') < 0)
            {
                int malformedFlag = source.IndexOf("&flag=meta", StringComparison.OrdinalIgnoreCase);
                if (malformedFlag >= 0)
                {
                    source = source.Substring(0, malformedFlag) + "?flag=meta"
                        + source.Substring(malformedFlag + "&flag=meta".Length);
                }
            }

            if (!Regex.IsMatch(source, @"(?:[?&])flag=meta(?:&|$)", RegexOptions.IgnoreCase))
            {
                source += source.IndexOf('?') >= 0 ? "&flag=meta" : "?flag=meta";
            }
            return source + fragment;
        }
    }

    internal static class OutputPathPolicy
    {
        public static void EnsureSafe(string outputPath, IEnumerable<string> localInputPaths,
            IEnumerable<string> protectedPaths)
        {
            foreach (string inputPath in localInputPaths ?? Enumerable.Empty<string>())
            {
                if (SamePath(outputPath, inputPath))
                    throw new InvalidOperationException(
                        "输出文件不能与输入配置文件相同，否则会覆盖原始配置：\n" + inputPath);
            }
            foreach (string protectedPath in protectedPaths ?? Enumerable.Empty<string>())
            {
                if (SamePath(outputPath, protectedPath))
                    throw new InvalidOperationException(
                        "输出文件不能覆盖程序运行文件：\n" + protectedPath);
            }
        }

        internal static bool SamePath(string left, string right)
        {
            if (string.IsNullOrWhiteSpace(left) || string.IsNullOrWhiteSpace(right)) return false;
            return string.Equals(Normalize(left), Normalize(right), StringComparison.OrdinalIgnoreCase);
        }

        private static string Normalize(string value)
        {
            string path = Path.GetFullPath(value.Trim());
            string root = Path.GetPathRoot(path) ?? "";
            while (path.Length > root.Length
                && (path.EndsWith(Path.DirectorySeparatorChar.ToString(), StringComparison.Ordinal)
                    || path.EndsWith(Path.AltDirectorySeparatorChar.ToString(), StringComparison.Ordinal)))
            {
                path = path.Substring(0, path.Length - 1);
            }
            return path;
        }
    }

    internal static class CommandLine
    {
        public static string Quote(string value)
        {
            if (value == null) return "\"\"";
            if (value.Length > 0 && !value.Any(delegate(char c) { return char.IsWhiteSpace(c) || c == '"' || c == '&'; }))
            {
                return value;
            }

            StringBuilder result = new StringBuilder();
            result.Append('"');
            int backslashes = 0;
            foreach (char c in value)
            {
                if (c == '\\')
                {
                    backslashes++;
                    continue;
                }
                if (c == '"')
                {
                    result.Append('\\', backslashes * 2 + 1);
                    result.Append('"');
                    backslashes = 0;
                    continue;
                }
                result.Append('\\', backslashes);
                backslashes = 0;
                result.Append(c);
            }
            result.Append('\\', backslashes * 2);
            result.Append('"');
            return result.ToString();
        }
    }

    internal static class LabelFormatter
    {
        private static readonly Dictionary<string, string> CountryNames =
            new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
            {
                {"US","美国"},{"CN","中国"},{"GB","英国"},{"UK","英国"},{"JP","日本"},{"DE","德国"},
                {"FR","法国"},{"RU","俄罗斯"},{"SG","新加坡"},{"HK","香港"},{"TW","台湾"},{"KR","韩国"},
                {"CA","加拿大"},{"AU","澳大利亚"},{"NL","荷兰"},{"IT","意大利"},{"ES","西班牙"},
                {"SE","瑞典"},{"NO","挪威"},{"DK","丹麦"},{"FI","芬兰"},{"CH","瑞士"},{"AT","奥地利"},
                {"BE","比利时"},{"BR","巴西"},{"IN","印度"},{"TH","泰国"},{"MY","马来西亚"},
                {"VN","越南"},{"PH","菲律宾"},{"ID","印度尼西亚"},{"UA","乌克兰"},{"TR","土耳其"},
                {"IL","以色列"},{"AE","阿联酋"},{"SA","沙特阿拉伯"},{"EG","埃及"},{"ZA","南非"},
                {"NG","尼日利亚"},{"KE","肯尼亚"},{"RO","罗马尼亚"},{"PL","波兰"},{"CZ","捷克"},
                {"HU","匈牙利"},{"BG","保加利亚"},{"HR","克罗地亚"},{"SI","斯洛文尼亚"},
                {"SK","斯洛伐克"},{"LT","立陶宛"},{"LV","拉脱维亚"},{"EE","爱沙尼亚"},
                {"PT","葡萄牙"},{"GR","希腊"},{"IE","爱尔兰"},{"LU","卢森堡"},{"MT","马耳他"},
                {"CY","塞浦路斯"},{"IS","冰岛"},{"MX","墨西哥"},{"AR","阿根廷"},{"CL","智利"},
                {"CO","哥伦比亚"},{"PE","秘鲁"},{"VE","委内瑞拉"},{"EC","厄瓜多尔"},
                {"UY","乌拉圭"},{"PY","巴拉圭"},{"BO","玻利维亚"},{"CR","哥斯达黎加"},
                {"PA","巴拿马"},{"GT","危地马拉"},{"HN","洪都拉斯"},{"SV","萨尔瓦多"},
                {"NI","尼加拉瓜"},{"BZ","伯利兹"},{"JM","牙买加"},{"TT","特立尼达和多巴哥"},
                {"NZ","新西兰"},{"MO","澳门"}
            };

        public static Dictionary<string, string> BuildRealExitRenameMap(
            IEnumerable<NodeManifestEvent> nodes, IDictionary<string, NodeSnapshot> snapshots)
        {
            Dictionary<string, string> renames =
                new Dictionary<string, string>(StringComparer.Ordinal);
            List<NodeManifestEvent> ordered = (nodes ?? Enumerable.Empty<NodeManifestEvent>())
                .Where(delegate(NodeManifestEvent node)
                {
                    return node != null && !string.IsNullOrWhiteSpace(node.id);
                }).ToList();
            HashSet<string> reserved = new HashSet<string>(ordered.Select(
                delegate(NodeManifestEvent node) { return node.name ?? ""; }), StringComparer.Ordinal);
            Dictionary<string, int> counters = new Dictionary<string, int>(StringComparer.OrdinalIgnoreCase);

            foreach (NodeManifestEvent node in ordered)
            {
                NodeSnapshot snapshot;
                if (snapshots == null || !snapshots.TryGetValue(node.id, out snapshot)
                    || snapshot == null
                    || !string.Equals(snapshot.RegionState, "成功", StringComparison.Ordinal)) continue;
                string code = (snapshot.RegionCountryCode ?? "").Trim().ToUpperInvariant();
                if (!Regex.IsMatch(code, @"\A[A-Z]{2}\z")) continue;
                reserved.Remove(node.name ?? "");
                int index;
                counters.TryGetValue(code, out index);
                string countryName;
                if (!CountryNames.TryGetValue(code, out countryName))
                    countryName = string.IsNullOrWhiteSpace(snapshot.RegionCountry)
                        ? code : snapshot.RegionCountry.Trim();
                string flag = string.IsNullOrWhiteSpace(snapshot.RegionEmoji)
                    ? CountryFlag(code) : snapshot.RegionEmoji.Trim();
                string formatted;
                do
                {
                    index++;
                    formatted = string.Format(CultureInfo.InvariantCulture, "{0} {1} {2}-{3}",
                        flag, countryName, code, index.ToString("D2", CultureInfo.InvariantCulture));
                }
                while (reserved.Contains(formatted));
                counters[code] = index;
                reserved.Add(formatted);
                if (!string.Equals(formatted, node.name, StringComparison.Ordinal)) renames[node.id] = formatted;
            }
            return renames;
        }

        private static string CountryFlag(string countryCode)
        {
            string normalized = string.Equals(countryCode, "UK", StringComparison.OrdinalIgnoreCase)
                ? "GB"
                : countryCode.ToUpperInvariant();
            if (normalized.Length != 2) return "\uD83C\uDFF3\uFE0F";
            return char.ConvertFromUtf32(0x1F1E6 + normalized[0] - 'A')
                + char.ConvertFromUtf32(0x1F1E6 + normalized[1] - 'A');
        }
    }

    internal sealed class GistUploadResult
    {
        public bool Created;
        public bool FileCreated;
        public string HtmlUrl;
        public string RawUrl;
    }

    internal sealed class GistInfo
    {
        public string Id;
        public string HtmlUrl;
        public bool FileExists;
    }

    internal static class GistClient
    {
        private const string ApiBaseUrl = "https://api.github.com";
        private const string GistDescriptionPrefix = "Clash-SpeedTest GUI: ";

        public static string TryExtractUsername(string address)
        {
            Uri uri;
            if (!Uri.TryCreate((address ?? "").Trim(), UriKind.Absolute, out uri)
                || !string.Equals(uri.Host, "gist.github.com", StringComparison.OrdinalIgnoreCase))
            {
                return "";
            }

            string[] segments = uri.AbsolutePath.Split(new[] { '/' }, StringSplitOptions.RemoveEmptyEntries);
            return segments.Length >= 2 ? segments[0] : "";
        }

        public static GistUploadResult CreateOrUpdate(string token, string username, string filePath)
        {
            return CreateOrUpdate(token, username, filePath, CancellationToken.None);
        }

        public static GistUploadResult CreateOrUpdate(
            string token, string username, string filePath, CancellationToken cancellationToken)
        {
            return CreateOrUpdate(token, username, filePath, cancellationToken, null);
        }

        internal static GistUploadResult CreateOrUpdate(
            string token, string username, string filePath, CancellationToken cancellationToken,
            HttpMessageHandler messageHandler)
        {
            cancellationToken.ThrowIfCancellationRequested();
            username = NormalizeUsername(username);
            string fileName = Path.GetFileName(filePath);
            cancellationToken.ThrowIfCancellationRequested();
            string content = File.ReadAllText(filePath, Encoding.UTF8);
            cancellationToken.ThrowIfCancellationRequested();
            JavaScriptSerializer serializer = new JavaScriptSerializer { MaxJsonLength = int.MaxValue };

            using (HttpClient client = CreateClient(token, messageHandler))
            {
                string userJson = Send(client, HttpMethod.Get, ApiBaseUrl + "/user", null,
                    "验证 GitHub 账号", cancellationToken);
                Dictionary<string, object> user = DeserializeObject(serializer, userJson, "GitHub 账号信息");
                string actualUsername = GetString(user, "login");
                if (!string.Equals(actualUsername, username, StringComparison.OrdinalIgnoreCase))
                {
                    throw new InvalidOperationException(
                        "Token 所属账号是 " + actualUsername + "，与填写的用户名 " + username + " 不一致。");
                }

                string gistId = "";
                string htmlUrl = "";
                GistInfo existing = FindDedicatedGist(
                    client, serializer, fileName, cancellationToken);
                if (existing != null)
                {
                    gistId = existing.Id;
                    htmlUrl = existing.HtmlUrl;
                }

                Dictionary<string, object> fileBody = new Dictionary<string, object>();
                fileBody["content"] = content;
                Dictionary<string, object> files = new Dictionary<string, object>();
                files[fileName] = fileBody;
                Dictionary<string, object> body = new Dictionary<string, object>();
                body["files"] = files;

                bool created = string.IsNullOrWhiteSpace(gistId);
                bool fileCreated = created || existing == null || !existing.FileExists;
                if (created)
                {
                    body["description"] = BuildGistDescription(fileName);
                    body["public"] = false;
                }

                string responseJson = Send(client,
                    created ? HttpMethod.Post : new HttpMethod("PATCH"),
                    created ? ApiBaseUrl + "/gists" : ApiBaseUrl + "/gists/" + gistId,
                    serializer.Serialize(body),
                    created ? "创建 Gist" : "更新 Gist", cancellationToken);
                Dictionary<string, object> response =
                    DeserializeObject(serializer, responseJson, created ? "Gist 创建结果" : "Gist 更新结果");
                string responseUrl = GetString(response, "html_url");
                if (!string.IsNullOrWhiteSpace(responseUrl)) htmlUrl = responseUrl;
                string responseId = GetString(response, "id");
                string resolvedGistId = string.IsNullOrWhiteSpace(responseId) ? gistId : responseId;
                if (string.IsNullOrWhiteSpace(resolvedGistId))
                    throw new InvalidOperationException("GitHub 未返回可用的 Gist ID。");
                if (string.IsNullOrWhiteSpace(htmlUrl))
                {
                    htmlUrl = "https://gist.github.com/" + actualUsername + "/" + resolvedGistId;
                }

                return new GistUploadResult
                {
                    Created = created,
                    FileCreated = fileCreated,
                    HtmlUrl = htmlUrl,
                    RawUrl = BuildStableRawUrl(actualUsername, resolvedGistId, fileName)
                };
            }
        }

        private static GistInfo FindDedicatedGist(
            HttpClient client, JavaScriptSerializer serializer, string fileName,
            CancellationToken cancellationToken)
        {
            const int pageSize = 100;
            for (int page = 1; ; page++)
            {
                cancellationToken.ThrowIfCancellationRequested();
                string listJson = Send(client, HttpMethod.Get,
                    string.Format(CultureInfo.InvariantCulture,
                        "{0}/gists?per_page={1}&page={2}", ApiBaseUrl, pageSize, page),
                    null, "查找程序专用 Gist", cancellationToken);
                object[] gists = serializer.DeserializeObject(listJson) as object[];
                if (gists == null)
                {
                    throw new InvalidOperationException("GitHub 返回了无法识别的 Gist 列表。");
                }

                GistInfo found = FindDedicatedGistInList(gists, fileName);
                if (found != null) return found;
                if (gists.Length < pageSize) return null;
            }
        }

        internal static GistInfo FindDedicatedGistInList(object[] gists, string fileName)
        {
            if (gists == null) return null;
            string targetDescription = BuildGistDescription(fileName);
            foreach (object item in gists)
            {
                Dictionary<string, object> gist = item as Dictionary<string, object>;
                if (gist != null
                    && string.Equals(GetString(gist, "description"), targetDescription,
                        StringComparison.Ordinal))
                {
                    object rawFiles;
                    Dictionary<string, object> files = gist.TryGetValue("files", out rawFiles)
                        ? rawFiles as Dictionary<string, object>
                        : null;
                    return new GistInfo
                    {
                        Id = GetString(gist, "id"),
                        HtmlUrl = GetString(gist, "html_url"),
                        FileExists = files != null && files.ContainsKey(fileName)
                    };
                }
            }
            return null;
        }

        internal static string BuildGistDescription(string fileName)
        {
            if (string.IsNullOrWhiteSpace(fileName))
                throw new InvalidOperationException("无法为无效的输出文件名创建 Gist。");
            return GistDescriptionPrefix + fileName;
        }

        internal static string BuildStableRawUrl(string username, string gistId, string fileName)
        {
            if (string.IsNullOrWhiteSpace(username)
                || string.IsNullOrWhiteSpace(gistId)
                || string.IsNullOrWhiteSpace(fileName))
                throw new InvalidOperationException("无法生成 Gist 文件订阅链接。");
            return "https://gist.githubusercontent.com/"
                + Uri.EscapeDataString(username) + "/"
                + Uri.EscapeDataString(gistId) + "/raw/"
                + Uri.EscapeDataString(fileName);
        }

        private static string NormalizeUsername(string value)
        {
            string username = (value ?? "").Trim();
            if (username.StartsWith("@", StringComparison.Ordinal)) username = username.Substring(1);
            if (!Regex.IsMatch(username, @"^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$")
                && !Regex.IsMatch(username, @"^[A-Za-z0-9]$"))
            {
                throw new InvalidOperationException("GitHub 用户名格式无效。");
            }
            return username;
        }

        private static HttpClient CreateClient(string token, HttpMessageHandler messageHandler)
        {
            HttpClient client = messageHandler == null
                ? new HttpClient()
                : new HttpClient(messageHandler, true);
            client.Timeout = TimeSpan.FromSeconds(30);
            client.DefaultRequestHeaders.UserAgent.ParseAdd("ClashSpeedTestGUI/1.0");
            client.DefaultRequestHeaders.Authorization = new AuthenticationHeaderValue("Bearer", token.Trim());
            client.DefaultRequestHeaders.Accept.Add(
                new MediaTypeWithQualityHeaderValue("application/vnd.github+json"));
            return client;
        }

        private static string Send(HttpClient client, HttpMethod method, string url, string json,
            string action, CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            using (HttpRequestMessage request = new HttpRequestMessage(method, url))
            {
                if (json != null)
                {
                    request.Content = new StringContent(json, Encoding.UTF8, "application/json");
                }
                try
                {
                    using (HttpResponseMessage response = client.SendAsync(request, cancellationToken)
                        .GetAwaiter().GetResult())
                    {
                        cancellationToken.ThrowIfCancellationRequested();
                        string responseText = response.Content.ReadAsStringAsync().GetAwaiter().GetResult();
                        cancellationToken.ThrowIfCancellationRequested();
                        if (!response.IsSuccessStatusCode)
                        {
                            throw new InvalidOperationException(string.Format(CultureInfo.InvariantCulture,
                                "{0}失败（HTTP {1}）：{2}", action, (int)response.StatusCode,
                                TrimResponse(responseText)));
                        }
                        return responseText;
                    }
                }
                catch (OperationCanceledException ex)
                {
                    if (cancellationToken.IsCancellationRequested) throw;
                    throw new TimeoutException(action + "超时（30 秒）。", ex);
                }
            }
        }

        private static Dictionary<string, object> DeserializeObject(
            JavaScriptSerializer serializer, string json, string name)
        {
            Dictionary<string, object> value = serializer.DeserializeObject(json) as Dictionary<string, object>;
            if (value == null) throw new InvalidOperationException(name + "格式无效。");
            return value;
        }

        private static string GetString(Dictionary<string, object> value, string key)
        {
            object raw;
            return value != null && value.TryGetValue(key, out raw) && raw != null
                ? Convert.ToString(raw, CultureInfo.InvariantCulture)
                : "";
        }

        private static string TrimResponse(string value)
        {
            if (string.IsNullOrWhiteSpace(value)) return "服务器未返回错误详情";
            value = value.Replace("\r", " ").Replace("\n", " ").Trim();
            return value.Length <= 300 ? value : value.Substring(0, 300) + "…";
        }
    }

    internal static class SelfTests
    {
        private sealed class BlockingGistHandler : HttpMessageHandler
        {
            private int requestCount;
            public readonly ManualResetEventSlim BlockingRequestStarted = new ManualResetEventSlim(false);
            public int RequestCount { get { return requestCount; } }

            protected override async Task<HttpResponseMessage> SendAsync(
                HttpRequestMessage request, CancellationToken cancellationToken)
            {
                int number = Interlocked.Increment(ref requestCount);
                if (number == 1 && request.RequestUri.AbsolutePath == "/user")
                {
                    return new HttpResponseMessage(HttpStatusCode.OK)
                    {
                        Content = new StringContent("{\"login\":\"tester\"}", Encoding.UTF8,
                            "application/json")
                    };
                }
                BlockingRequestStarted.Set();
                await Task.Delay(Timeout.Infinite, cancellationToken).ConfigureAwait(false);
                throw new InvalidOperationException("阻塞请求不应正常完成。");
            }

            protected override void Dispose(bool disposing)
            {
                if (disposing) BlockingRequestStarted.Dispose();
                base.Dispose(disposing);
            }
        }

        private sealed class IndependentlyCanceledGistHandler : HttpMessageHandler
        {
            public int RequestCount;

            protected override Task<HttpResponseMessage> SendAsync(
                HttpRequestMessage request, CancellationToken cancellationToken)
            {
                Interlocked.Increment(ref RequestCount);
                TaskCompletionSource<HttpResponseMessage> completion =
                    new TaskCompletionSource<HttpResponseMessage>();
                completion.SetCanceled();
                return completion.Task;
            }
        }

        public static bool Run()
        {
            try
            {
                NodeManifestEvent renameFixture = new NodeManifestEvent
                {
                    id = "stable-id",
                    name = "original",
                    config = new Dictionary<string, object>
                    {
                        { "name", "original" },
                        { "server", "us-entry.example.com" },
                        { "password", "secret" }
                    }
                };
                NodeManifestEvent failedRenameFixture = new NodeManifestEvent
                {
                    id = "failed-id",
                    name = "keep-original",
                    config = new Dictionary<string, object> { { "name", "keep-original" } }
                };
                Dictionary<string, string> renameMap =
                    LabelFormatter.BuildRealExitRenameMap(
                        new[] { renameFixture, failedRenameFixture },
                        new Dictionary<string, NodeSnapshot>
                        {
                            { "stable-id", new NodeSnapshot
                                {
                                    Id = "stable-id", RegionState = "成功",
                                    RegionCountryCode = "HK", RegionCountry = "Hong Kong",
                                    RegionEmoji = "🇭🇰"
                                }
                            },
                            { "failed-id", new NodeSnapshot
                                {
                                    Id = "failed-id", RegionState = "查询失败",
                                    RegionCountryCode = "US", RegionCountry = "美国"
                                }
                            }
                        });
                Assert(renameMap.Count == 1 && renameMap["stable-id"] == "🇭🇰 香港 HK-01"
                    && !renameMap.ContainsKey("failed-id")
                    && Convert.ToString(renameFixture.config["password"], CultureInfo.InvariantCulture)
                        == "secret",
                    "真实出口地区覆盖入口地址，单节点失败保留原名且不改凭据字段");
                Assert(GistClient.TryExtractUsername("https://gist.github.com/user/abc123") == "user",
                    "旧版 Gist 地址迁移用户名");
                TestGistSelection();
                TestGistCancellation();
                Assert(CommandLine.Quote("C:\\A B\\x.yaml") == "\"C:\\A B\\x.yaml\"", "参数引用");
                Assert(SubscriptionUrl.NormalizeSources("https://example.com/sub/token")
                    == "https://example.com/sub/token?flag=meta", "无查询参数订阅");
                Assert(SubscriptionUrl.NormalizeSources("https://example.com/sub?token=x")
                    == "https://example.com/sub?token=x&flag=meta", "已有查询参数订阅");
                Assert(SubscriptionUrl.NormalizeSources("https://example.com/sub/token&flag=meta")
                    == "https://example.com/sub/token?flag=meta", "错误分隔符修复");
                Assert(SubscriptionUrl.IsInlineNode(
                    "vless://id@example.com:443?security=tls#node"), "单节点链接识别");
                Assert(!SubscriptionUrl.IsInlineNode(
                    "https://example.com/subscription"), "HTTP 订阅保持远程读取");
                List<string> batchNodes = SubscriptionUrl.GetSources(
                    "vless://id@example.com:443?alpn=h2,http/1.1#one\r\n"
                    + "trojan://password@example.org:443#two\r\n\r\n");
                Assert(batchNodes.Count == 2, "多行节点拆分");
                Assert(batchNodes[0].Contains("alpn=h2,http/1.1"), "节点参数中的逗号保留");
                List<string> commaUrl = SubscriptionUrl.GetSources(
                    "https://one.example/sub,part?token=a,b");
                Assert(commaUrl.Count == 1
                    && commaUrl[0] == "https://one.example/sub,part?token=a,b",
                    "订阅 URL 中的逗号原样保留");
                List<string> commaPath = SubscriptionUrl.GetSources(
                    "C:\\configs\\work,travel.yaml\r\nC:\\configs\\backup.yaml");
                Assert(commaPath.Count == 2 && commaPath[0].EndsWith("work,travel.yaml"),
                    "Windows 文件名中的逗号原样保留，每行一个输入");
                TestOutputPathPolicy();
                Assert(!OutputPolicy.ShouldCommit(10, 0), "空结果不覆盖输出");
                Assert(OutputPolicy.ShouldCommit(10, 1), "有效结果允许写入");
                Assert(CompletionStatus.Format(68, 29)
                    == "测速完成：68/68，有效 29 个，失败 39 个", "完成状态统计");
                TestTaskOperationAndBatchQueue();
                TestChildProcessCancellation();
                TestRunnerProtocolValidation();
                TestV5ResultProjection();
                TestRegionProtocolValidation();
                TestRegionProtocolProcessBoundaries();
                TestNodeFeatures();
                SpeedPreset balanced = SpeedPresets.Get(1);
                Assert(balanced != null && balanced.Mode == "download"
                    && balanced.DownloadSizeMb == 20, "均衡测速方案");
                AppSettings defaults = AppSettings.CreateDefault();
                Assert(defaults.MinDownloadSpeed == 3 && defaults.SpeedMode == balanced.Mode
                    && defaults.MaxLatencyMs == balanced.MaxLatencyMs
                    && defaults.DownloadSizeMb == balanced.DownloadSizeMb
                    && defaults.ProbeTimeoutSeconds == balanced.ProbeTimeoutSeconds
                    && defaults.TimeoutSeconds == balanced.TimeoutSeconds
                    && defaults.Concurrent == balanced.NodeConcurrent
                    && defaults.TransferConcurrent == balanced.TransferConcurrent
                    && defaults.MaxHTTPProbeFailure == balanced.MaxHTTPProbeFailure,
                    "新安装默认参数匹配均衡（推荐）方案");
                TestSettingsStoreOverride();
                TestAtomicFileCommit();
                return true;
            }
            catch (Exception ex)
            {
                Console.Error.WriteLine(ex.ToString());
                return false;
            }
        }

        private static void Assert(bool condition, string name)
        {
            if (!condition) throw new InvalidOperationException("自测失败：" + name);
        }

        private static void AssertInvalidOperation(Action action, string name)
        {
            bool rejected = false;
            try
            {
                action();
            }
            catch (InvalidOperationException)
            {
                rejected = true;
            }
            Assert(rejected, name);
        }

        private static void AssertOperationCanceled(Action action, string name)
        {
            bool canceled = false;
            try
            {
                action();
            }
            catch (OperationCanceledException)
            {
                canceled = true;
            }
            Assert(canceled, name);
        }

        private static void TestTaskOperationAndBatchQueue()
        {
            Assert(DpiAwarenessProbe.Current() >= 1, "进程启用原生 DPI 感知");
            Assert(OptionsLayoutPolicy.PanelHeight(661, 22, 635, 421, 170) == 469,
                "最小窗口展开高级设置时为结果表格保留高度");
            Assert(OptionsLayoutPolicy.PanelHeight(781, 22, 421, 421, 170) == 421,
                "默认窗口折叠后为结果表格增加高度");
            using (TaskOperation operation = new TaskOperation(7, TaskOperationKind.SpeedTest))
            {
                Assert(TaskControlPolicy.InputsEnabled(false), "空闲时允许编辑任务设置");
                Assert(!TaskControlPolicy.InputsEnabled(true), "忙碌时锁定任务设置");
                Assert(TaskControlPolicy.StopEnabled(operation), "运行任务允许停止");
                operation.Cancel();
                Assert(operation.Token.IsCancellationRequested, "任务取消令牌生效");
                Assert(!TaskControlPolicy.StopEnabled(operation), "取消后停止按钮禁用");
            }
            using (TaskOperation management =
                new TaskOperation(8, TaskOperationKind.NodeManagement))
            {
                Assert(TaskControlPolicy.StopEnabled(management), "节点管理任务允许停止");
                management.Cancel();
                Assert(!TaskControlPolicy.StopEnabled(management),
                    "节点管理取消后停止按钮禁用");
            }

            NodeUiUpdateQueue queue = new NodeUiUpdateQueue();
            queue.Enqueue(new NodeUiUpdate
            {
                OperationId = 1,
                Kind = NodeUiUpdateKind.Manifest,
                Node = new NodeSnapshot { Id = "old" }
            });
            queue.Enqueue(new NodeUiUpdate
            {
                OperationId = 2,
                Kind = NodeUiUpdateKind.Manifest,
                Node = new NodeSnapshot { Id = "node" }
            });
            queue.Enqueue(new NodeUiUpdate
            {
                OperationId = 2,
                Kind = NodeUiUpdateKind.Result,
                Result = new NodeResultEvent
                {
                    id = "node",
                    cells = new[] { "1.", "node", "ss", "20ms" },
                    usable = true,
                    metrics = CreateFastResultMetrics()
                }
            });
            List<NodeUiUpdate> stale = queue.Drain(2, 1);
            List<NodeUiUpdate> first = queue.Drain(2, 1);
            List<NodeUiUpdate> second = queue.Drain(2, 200);
            Assert(stale.Count == 0, "旧任务事件也消耗单批扫描预算");
            Assert(first.Count == 1 && first[0].Kind == NodeUiUpdateKind.Manifest,
                "批处理队列丢弃旧任务并保持顺序");
            Assert(second.Count == 1 && second[0].Kind == NodeUiUpdateKind.Result && queue.Count == 0,
                "批处理队列遵守单批上限");

            NodeUiUpdateQueue largeQueue = new NodeUiUpdateQueue();
            for (int i = 0; i < 1000; i++)
            {
                largeQueue.Enqueue(new NodeUiUpdate
                {
                    OperationId = 9,
                    Kind = NodeUiUpdateKind.Manifest,
                    Node = new NodeSnapshot { Id = "node-" + i }
                });
            }
            int batches = 0;
            int drained = 0;
            while (largeQueue.Count > 0)
            {
                List<NodeUiUpdate> batch = largeQueue.Drain(9, 200);
                batches++;
                drained += batch.Count;
            }
            Assert(batches == 5 && drained == 1000,
                "千节点队列按每批二百条稳定排空");
        }

        private static void TestChildProcessCancellation()
        {
            string preCanceledEventName = "Local\\ClashSpeedTestGUI-PreCanceled-"
                + Guid.NewGuid().ToString("N");
            using (EventWaitHandle ready = new EventWaitHandle(
                false, EventResetMode.ManualReset, preCanceledEventName))
            using (CancellationTokenSource cancellation = new CancellationTokenSource())
            using (Process process = CreateBlockingSelfTestProcess(preCanceledEventName))
            {
                cancellation.Cancel();
                AssertOperationCanceled(delegate
                {
                    using (ChildProcessLifetime.Start(process, cancellation.Token)) { }
                }, "预取消令牌阻止子进程启动");
                bool processWasNotStarted = false;
                try { int ignored = process.Id; }
                catch (InvalidOperationException) { processWasNotStarted = true; }
                Assert(processWasNotStarted && !ready.WaitOne(100),
                    "预取消不会创建阻塞测试子进程");
            }

            for (int readMode = 0; readMode < 3; readMode++)
                AssertBlockingChildCancellation(readMode);

            string terminateEventName = "Local\\ClashSpeedTestGUI-Terminate-"
                + Guid.NewGuid().ToString("N");
            using (EventWaitHandle ready = new EventWaitHandle(
                false, EventResetMode.ManualReset, terminateEventName))
            using (Process process = CreateBlockingSelfTestProcess(terminateEventName))
            {
                using (ChildProcessLease lease =
                    ChildProcessLifetime.Start(process, CancellationToken.None))
                {
                    Assert(ready.WaitOne(5000), "异常清理测试子进程按时启动");
                }
                Assert(process.HasExited, "消费者异常清理会终止仍运行的子进程");
            }
        }

        private static Process CreateBlockingSelfTestProcess(string readyEventName)
        {
            return new Process
            {
                StartInfo = new ProcessStartInfo
                {
                    FileName = Application.ExecutablePath,
                    Arguments = "--self-test-block-child " + CommandLine.Quote(readyEventName),
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    RedirectStandardOutput = true,
                    RedirectStandardError = true,
                    StandardOutputEncoding = Encoding.UTF8,
                    StandardErrorEncoding = Encoding.UTF8
                }
            };
        }

        private static void TestRegionProtocolProcessBoundaries()
        {
            JavaScriptSerializer serializer = new JavaScriptSerializer();

            string malformedEventName = "Local\\ClashSpeedTestGUI-RegionMalformed-"
                + Guid.NewGuid().ToString("N");
            using (EventWaitHandle ready = new EventWaitHandle(
                false, EventResetMode.ManualReset, malformedEventName))
            using (Process process = CreateRegionSelfTestProcess("malformed-block", malformedEventName))
            {
                bool rejected = false;
                try
                {
                    using (ChildProcessLease lease =
                        ChildProcessLifetime.Start(process, CancellationToken.None))
                    {
                        Assert(ready.WaitOne(5000), "畸形地区协议子进程按时启动");
                        RegionProtocolValidator validator =
                            new RegionProtocolValidator(new[] { "node-a" });
                        string line;
                        while ((line = process.StandardOutput.ReadLine()) != null)
                            RegionProtocolLineParser.AcceptLine(line, validator, serializer);
                        process.WaitForExit();
                        lease.Complete();
                    }
                }
                catch (InvalidOperationException)
                {
                    rejected = true;
                }
                Assert(rejected && process.WaitForExit(3000) && process.HasExited,
                    "畸形地区协议立即失败并终止阻塞子进程");
            }

            string missingEventName = "Local\\ClashSpeedTestGUI-RegionMissing-"
                + Guid.NewGuid().ToString("N");
            using (EventWaitHandle ready = new EventWaitHandle(
                false, EventResetMode.ManualReset, missingEventName))
            using (Process process = CreateRegionSelfTestProcess("missing-exit", missingEventName))
            {
                RegionProtocolValidator validator = new RegionProtocolValidator(new[] { "node-a" });
                using (ChildProcessLease lease =
                    ChildProcessLifetime.Start(process, CancellationToken.None))
                {
                    string line;
                    int lineCount = 0;
                    while ((line = process.StandardOutput.ReadLine()) != null)
                    {
                        RegionProtocolLineParser.AcceptLine(line, validator, serializer);
                        lineCount++;
                    }
                    process.WaitForExit();
                    lease.Complete();
                    Assert(process.ExitCode == 0 && lineCount == 2,
                        "缺失地区结果样例输出表头后正常退出");
                }
                AssertInvalidOperation(delegate { validator.ValidateCompletion(); },
                    "退出码零但缺失地区事件会被完整性校验拒绝");
            }

            string nonzeroEventName = "Local\\ClashSpeedTestGUI-RegionNonzero-"
                + Guid.NewGuid().ToString("N");
            using (EventWaitHandle ready = new EventWaitHandle(
                false, EventResetMode.ManualReset, nonzeroEventName))
            using (Process process = CreateRegionSelfTestProcess("partial-nonzero", nonzeroEventName))
            {
                RegionProtocolValidator validator = new RegionProtocolValidator(new[] { "node-a" });
                using (ChildProcessLease lease =
                    ChildProcessLifetime.Start(process, CancellationToken.None))
                {
                    string line;
                    int lineCount = 0;
                    while ((line = process.StandardOutput.ReadLine()) != null)
                    {
                        RegionProtocolLineParser.AcceptLine(line, validator, serializer);
                        lineCount++;
                    }
                    process.WaitForExit();
                    lease.Complete();
                    Assert(process.ExitCode == 7 && lineCount == 2 && !validator.IsComplete,
                        "非零退出优先保留进程错误，不误判为成功流");
                }
            }

            string cancelEventName = "Local\\ClashSpeedTestGUI-RegionCancel-"
                + Guid.NewGuid().ToString("N");
            using (EventWaitHandle ready = new EventWaitHandle(
                false, EventResetMode.ManualReset, cancelEventName))
            using (CancellationTokenSource cancellation = new CancellationTokenSource())
            using (Process process = CreateRegionSelfTestProcess("missing-block", cancelEventName))
            {
                bool canceled = false;
                using (ChildProcessLease lease =
                    ChildProcessLifetime.Start(process, cancellation.Token))
                {
                    Assert(ready.WaitOne(5000), "取消地区协议子进程按时启动");
                    RegionProtocolValidator validator =
                        new RegionProtocolValidator(new[] { "node-a" });
                    RegionProtocolLineParser.AcceptLine(
                        process.StandardOutput.ReadLine(), validator, serializer);
                    RegionProtocolLineParser.AcceptLine(
                        process.StandardOutput.ReadLine(), validator, serializer);
                    cancellation.CancelAfter(100);
                    Assert(process.StandardOutput.ReadLine() == null,
                        "取消会解除地区协议阻塞读取");
                    try { cancellation.Token.ThrowIfCancellationRequested(); }
                    catch (OperationCanceledException) { canceled = true; }
                }
                Assert(canceled && process.WaitForExit(3000) && process.HasExited,
                    "取消地区查询跳过完整性提交并终止子进程");
            }
        }

        private static Process CreateRegionSelfTestProcess(string mode, string readyEventName)
        {
            return new Process
            {
                StartInfo = new ProcessStartInfo
                {
                    FileName = Application.ExecutablePath,
                    Arguments = "--self-test-region-child " + CommandLine.Quote(mode)
                        + " " + CommandLine.Quote(readyEventName),
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    RedirectStandardOutput = true,
                    RedirectStandardError = true,
                    StandardOutputEncoding = Encoding.UTF8,
                    StandardErrorEncoding = Encoding.UTF8
                }
            };
        }

        private static void AssertBlockingChildCancellation(int readMode)
        {
            string eventName = "Local\\ClashSpeedTestGUI-Blocking-"
                + readMode + "-" + Guid.NewGuid().ToString("N");
            using (EventWaitHandle ready = new EventWaitHandle(
                false, EventResetMode.ManualReset, eventName))
            using (CancellationTokenSource cancellation = new CancellationTokenSource())
            using (ManualResetEventSlim enteredRead = new ManualResetEventSlim(false))
            using (Process process = CreateBlockingSelfTestProcess(eventName))
            {
                Exception observed = null;
                Task worker = Task.Run(delegate
                {
                    try
                    {
                        using (ChildProcessLease lease =
                            ChildProcessLifetime.Start(process, cancellation.Token))
                        {
                            if (readMode == 0)
                            {
                                Task<string> output = process.StandardOutput.ReadToEndAsync();
                                Task<string> error = process.StandardError.ReadToEndAsync();
                                enteredRead.Set();
                                process.WaitForExit();
                                Task.WaitAll(output, error);
                            }
                            else if (readMode == 1)
                            {
                                process.OutputDataReceived += delegate { };
                                process.ErrorDataReceived += delegate { };
                                process.BeginOutputReadLine();
                                process.BeginErrorReadLine();
                                enteredRead.Set();
                                process.WaitForExit();
                                process.WaitForExit();
                            }
                            else
                            {
                                enteredRead.Set();
                                process.StandardOutput.ReadLine();
                                process.WaitForExit();
                            }
                            lease.Complete();
                            cancellation.Token.ThrowIfCancellationRequested();
                        }
                    }
                    catch (Exception ex)
                    {
                        observed = ex;
                    }
                });

                try
                {
                    Assert(ready.WaitOne(5000), "阻塞测试子进程按时启动，读取模式 " + readMode);
                    Assert(enteredRead.Wait(5000),
                        "父进程已进入目标读取路径，读取模式 " + readMode);
                    Assert(!process.HasExited,
                        "取消前阻塞测试子进程仍在运行，读取模式 " + readMode);
                    Thread.Sleep(100);
                    cancellation.Cancel();
                    Assert(worker.Wait(5000), "取消解除子进程读取阻塞，读取模式 " + readMode);
                    Assert(observed is OperationCanceledException,
                        "子进程读取取消返回 OperationCanceledException，读取模式 " + readMode);
                    Assert(process.HasExited, "取消后子进程已经退出，读取模式 " + readMode);
                }
                finally
                {
                    if (!cancellation.IsCancellationRequested) cancellation.Cancel();
                    ChildProcessLifetime.Terminate(process, 2000);
                    if (!worker.IsCompleted) worker.Wait(1000);
                }
            }
        }

        private static void TestRunnerProtocolValidation()
        {
            string[] headers = { "序号", "节点名称", "类型", "HTTP 延迟" };
            NodeManifestEvent node = new NodeManifestEvent
            {
                id = "stable-id",
                name = "node-a",
                type = "ss",
                config = new Dictionary<string, object>
                {
                    { "name", "node-a" },
                    { "type", "ss" }
                }
            };
            NodeResultEvent result = new NodeResultEvent
            {
                id = "stable-id",
                cells = new[] { "1.", "node-a", "ss", "20ms" },
                usable = true,
                metrics = CreateFastResultMetrics()
            };

            RunnerProtocolValidator valid = new RunnerProtocolValidator(headers);
            valid.AcceptProtocol(5);
            valid.AcceptHeader(headers);
            valid.AcceptNodeCount(1);
            valid.AcceptNode(node);
            valid.AcceptLegacyNodeMirror("node-a", "ss");
            valid.AcceptProgress(new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
            valid.AcceptResult(result);
            valid.AcceptLegacyResultMirror(result.cells);
            valid.ValidateCompletion();
            Assert(valid.IsComplete && valid.ResultCount == 1, "完整 v5 协议通过");
            JavaScriptSerializer protocolSerializer = new JavaScriptSerializer();
            string resultJson = protocolSerializer.Serialize(result);
            RunnerProtocolValidator.ValidateResultEnvelope(
                protocolSerializer.DeserializeObject(resultJson) as Dictionary<string, object>);
            RunnerProtocolValidator.ValidateProgressEnvelope(
                protocolSerializer.DeserializeObject("{\"id\":\"stable-id\",\"stage\":\"probe_completed\"}")
                    as Dictionary<string, object>);
            NodeResultEvent resultRoundTrip =
                protocolSerializer.Deserialize<NodeResultEvent>(resultJson);
            Assert(resultRoundTrip.usable == true
                && resultRoundTrip.metrics.latency_nanoseconds == 20000000,
                "v5 结果 JSON 固定结构可完整回读");

            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator.ValidateProgressEnvelope(
                    protocolSerializer.DeserializeObject(
                        "{\"id\":\"stable-id\",\"stage\":\"probe_completed\",\"extra\":true}")
                        as Dictionary<string, object>);
            }, "拒绝 v5 进度事件中的未知字段");
            AssertInvalidOperation(delegate
            {
                new RunnerProtocolValidator(headers).AcceptProtocol(3);
            }, "拒绝旧协议版本");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator wrongHeader = new RunnerProtocolValidator(headers);
                wrongHeader.AcceptProtocol(5);
                wrongHeader.AcceptHeader(new[] { "序号", "节点名称", "类型", "下载速度" });
            }, "拒绝不匹配表头");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator incomplete = new RunnerProtocolValidator(headers);
                incomplete.AcceptProtocol(5);
                incomplete.AcceptHeader(headers);
                incomplete.AcceptNodeCount(1);
                incomplete.AcceptNode(node);
                incomplete.ValidateCompletion();
            }, "拒绝缺失结果的成功终态");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator duplicate = new RunnerProtocolValidator(headers);
                duplicate.AcceptProtocol(5);
                duplicate.AcceptHeader(headers);
                duplicate.AcceptNodeCount(1);
                duplicate.AcceptNode(node);
                duplicate.AcceptNode(node);
            }, "拒绝重复节点 ID");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator unknown = new RunnerProtocolValidator(headers);
                unknown.AcceptProtocol(5);
                unknown.AcceptHeader(headers);
                unknown.AcceptNodeCount(1);
                unknown.AcceptNode(node);
                unknown.AcceptResult(new NodeResultEvent
                {
                    id = "missing",
                    cells = new[] { "1.", "node-a", "ss", "20ms" },
                    usable = true,
                    metrics = CreateFastResultMetrics()
                });
            }, "拒绝未知结果 ID");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator unknownProgress = new RunnerProtocolValidator(headers);
                unknownProgress.AcceptProtocol(5);
                unknownProgress.AcceptHeader(headers);
                unknownProgress.AcceptNodeCount(1);
                unknownProgress.AcceptNode(node);
                unknownProgress.AcceptProgress(
                    new NodeProgressEvent { id = "missing", stage = "probe_completed" });
            }, "拒绝未知进度 ID");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator duplicateProgress = new RunnerProtocolValidator(headers);
                duplicateProgress.AcceptProtocol(5);
                duplicateProgress.AcceptHeader(headers);
                duplicateProgress.AcceptNodeCount(1);
                duplicateProgress.AcceptNode(node);
                duplicateProgress.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                duplicateProgress.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
            }, "拒绝重复 probe_completed");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator unknownStage = new RunnerProtocolValidator(headers);
                unknownStage.AcceptProtocol(5);
                unknownStage.AcceptHeader(headers);
                unknownStage.AcceptNodeCount(1);
                unknownStage.AcceptNode(node);
                unknownStage.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "other" });
            }, "拒绝未知进度阶段");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator fastDownload = new RunnerProtocolValidator(headers);
                fastDownload.AcceptProtocol(5);
                fastDownload.AcceptHeader(headers);
                fastDownload.AcceptNodeCount(1);
                fastDownload.AcceptNode(node);
                fastDownload.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                fastDownload.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "download_started" });
            }, "拒绝快速模式 download_started");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator earlyResult = new RunnerProtocolValidator(headers);
                earlyResult.AcceptProtocol(5);
                earlyResult.AcceptHeader(headers);
                earlyResult.AcceptNodeCount(1);
                earlyResult.AcceptNode(node);
                earlyResult.AcceptResult(result);
            }, "拒绝 probe_completed 之前的最终结果");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator halfOutput = new RunnerProtocolValidator(headers);
                halfOutput.AcceptProtocol(5);
                halfOutput.AcceptHeader(headers);
                halfOutput.AcceptNodeCount(1);
                halfOutput.AcceptNode(node);
                halfOutput.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                halfOutput.ValidateCompletion();
            }, "拒绝半截协议输出");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator duplicateResult = new RunnerProtocolValidator(headers);
                duplicateResult.AcceptProtocol(5);
                duplicateResult.AcceptHeader(headers);
                duplicateResult.AcceptNodeCount(1);
                duplicateResult.AcceptNode(node);
                duplicateResult.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                duplicateResult.AcceptResult(result);
                duplicateResult.AcceptResult(result);
            }, "拒绝重复最终结果");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator mirror = new RunnerProtocolValidator(headers);
                mirror.AcceptProtocol(5);
                mirror.AcceptHeader(headers);
                mirror.AcceptNodeCount(1);
                mirror.AcceptNode(node);
                mirror.AcceptLegacyNodeMirror("other", "ss");
            }, "拒绝与事件不一致的兼容节点行");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator mirror = new RunnerProtocolValidator(headers);
                mirror.AcceptProtocol(5);
                mirror.AcceptHeader(headers);
                mirror.AcceptNodeCount(1);
                mirror.AcceptNode(node);
                mirror.AcceptProgress(new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                mirror.AcceptResult(result);
                mirror.AcceptLegacyResultMirror(result.cells);
                mirror.AcceptLegacyResultMirror(result.cells);
            }, "拒绝重复的兼容结果行");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator invalidConfig = new RunnerProtocolValidator(headers);
                invalidConfig.AcceptProtocol(5);
                invalidConfig.AcceptHeader(headers);
                invalidConfig.AcceptNodeCount(1);
                invalidConfig.AcceptNode(new NodeManifestEvent
                {
                    id = "bad-config",
                    name = "node-a",
                    type = "ss",
                    config = new Dictionary<string, object> { { "name", "node-a" } }
                });
            }, "拒绝缺少类型的节点配置");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator nullCell = new RunnerProtocolValidator(headers);
                nullCell.AcceptProtocol(5);
                nullCell.AcceptHeader(headers);
                nullCell.AcceptNodeCount(1);
                nullCell.AcceptNode(node);
                nullCell.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                nullCell.AcceptResult(new NodeResultEvent
                {
                    id = "stable-id",
                    cells = new[] { "1.", "node-a", "ss", (string)null },
                    usable = true,
                    metrics = CreateFastResultMetrics()
                });
            }, "拒绝结果中的空单元格");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator missingMetrics = new RunnerProtocolValidator(headers);
                missingMetrics.AcceptProtocol(5);
                missingMetrics.AcceptHeader(headers);
                missingMetrics.AcceptNodeCount(1);
                missingMetrics.AcceptNode(node);
                missingMetrics.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                missingMetrics.AcceptResult(new NodeResultEvent
                {
                    id = "stable-id",
                    cells = new[] { "1.", "node-a", "ss", "20ms" },
                    usable = true
                });
            }, "拒绝缺少原始指标的 v5 结果");
            AssertInvalidOperation(delegate
            {
                Dictionary<string, object> envelope = new Dictionary<string, object>
                {
                    { "id", "stable-id" },
                    { "cells", new object[0] },
                    { "usable", true },
                    { "metrics", new Dictionary<string, object>
                        {
                            { "latency_nanoseconds", 1 },
                            { "jitter_nanoseconds", 0 },
                            { "http_probe_failure_percent", 0 },
                            { "download_bytes_per_second", 0 },
                            { "download_tested", false },
                            { "download_complete", false }
                        }
                    },
                    { "unknown", true }
                };
                RunnerProtocolValidator.ValidateResultEnvelope(envelope);
            }, "拒绝 v5 结果中的未知字段");
            AssertInvalidOperation(delegate
            {
                Dictionary<string, object> wrongType =
                    protocolSerializer.DeserializeObject(resultJson) as Dictionary<string, object>;
                wrongType["usable"] = "true";
                RunnerProtocolValidator.ValidateResultEnvelope(wrongType);
            }, "拒绝 v5 结果中可自动转换但类型错误的字段");
            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator invalidMetrics = new RunnerProtocolValidator(headers);
                invalidMetrics.AcceptProtocol(5);
                invalidMetrics.AcceptHeader(headers);
                invalidMetrics.AcceptNodeCount(1);
                invalidMetrics.AcceptNode(node);
                invalidMetrics.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                NodeResultMetricsEvent metrics = CreateFastResultMetrics();
                metrics.http_probe_failure_percent = 101;
                invalidMetrics.AcceptResult(new NodeResultEvent
                {
                    id = "stable-id",
                    cells = new[] { "1.", "node-a", "ss", "20ms" },
                    usable = false,
                    metrics = metrics
                });
            }, "拒绝越界的 v5 原始指标");

            string[] downloadHeaders =
            {
                "序号", "节点名称", "类型", "HTTP 延迟", "抖动", "HTTP 探测失败率", "下载速度"
            };
            NodeResultEvent partialResult = new NodeResultEvent
            {
                id = "stable-id",
                cells = new[] { "1.", "node-a", "ss", "20ms", "1ms", "0%", "2.00 MB/s" },
                usable = false,
                metrics = CreatePartialDownloadMetrics()
            };
            RunnerProtocolValidator partial = new RunnerProtocolValidator(downloadHeaders);
            partial.AcceptProtocol(5);
            partial.AcceptHeader(downloadHeaders);
            partial.AcceptNodeCount(1);
            partial.AcceptNode(node);
            partial.AcceptProgress(new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
            partial.AcceptProgress(new NodeProgressEvent { id = "stable-id", stage = "download_started" });
            partial.AcceptResult(partialResult);
            partial.ValidateCompletion();
            Assert(partial.IsComplete, "v5 允许显示已启动但未完成的部分传输速度");

            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator outOfOrder = new RunnerProtocolValidator(downloadHeaders);
                outOfOrder.AcceptProtocol(5);
                outOfOrder.AcceptHeader(downloadHeaders);
                outOfOrder.AcceptNodeCount(1);
                outOfOrder.AcceptNode(node);
                outOfOrder.AcceptProgress(
                    new NodeProgressEvent { id = "stable-id", stage = "download_started" });
            }, "拒绝 probe_completed 之前的 download_started");

            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator unusableCompletion =
                    new RunnerProtocolValidator(downloadHeaders);
                unusableCompletion.AcceptProtocol(5);
                unusableCompletion.AcceptHeader(downloadHeaders);
                unusableCompletion.AcceptNodeCount(1);
                unusableCompletion.AcceptNode(node);
                unusableCompletion.AcceptProgress(new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                unusableCompletion.AcceptProgress(new NodeProgressEvent { id = "stable-id", stage = "download_started" });
                partialResult.usable = true;
                try { unusableCompletion.AcceptResult(partialResult); }
                finally { partialResult.usable = false; }
            }, "v5 未完成计划传输的节点不能标记 usable");

            AssertInvalidOperation(delegate
            {
                RunnerProtocolValidator impossibleCompletion =
                    new RunnerProtocolValidator(downloadHeaders);
                impossibleCompletion.AcceptProtocol(5);
                impossibleCompletion.AcceptHeader(downloadHeaders);
                impossibleCompletion.AcceptNodeCount(1);
                impossibleCompletion.AcceptNode(node);
                impossibleCompletion.AcceptProgress(new NodeProgressEvent { id = "stable-id", stage = "probe_completed" });
                impossibleCompletion.AcceptProgress(new NodeProgressEvent { id = "stable-id", stage = "download_started" });
                NodeResultMetricsEvent metrics = CreatePartialDownloadMetrics();
                metrics.download_tested = false;
                metrics.download_complete = true;
                metrics.download_bytes_per_second = 0;
                impossibleCompletion.AcceptResult(new NodeResultEvent
                {
                    id = "stable-id",
                    cells = partialResult.cells,
                    usable = false,
                    metrics = metrics
                });
            }, "v5 complete 不能在 tested 之前成立");
        }

        private static NodeResultMetricsEvent CreateFastResultMetrics()
        {
            return new NodeResultMetricsEvent
            {
                latency_nanoseconds = 20000000,
                jitter_nanoseconds = 1000000,
                http_probe_failure_percent = 0,
                download_bytes_per_second = 0,
                download_tested = false,
                download_complete = false
            };
        }

        private static NodeResultMetricsEvent CreatePartialDownloadMetrics()
        {
            return new NodeResultMetricsEvent
            {
                latency_nanoseconds = 20000000,
                jitter_nanoseconds = 1000000,
                http_probe_failure_percent = 0,
                download_bytes_per_second = 2D * 1024D * 1024D,
                download_tested = true,
                download_complete = false
            };
        }

        private static void TestV5ResultProjection()
        {
            NodeSnapshot node = new NodeSnapshot();
            NodeResultEvent boundary = new NodeResultEvent
            {
                id = "boundary",
                cells = new[]
                {
                    "1.", "boundary", "ss", "1000ms", "0ms", "0.0%", "5.00MB/s"
                },
                usable = false,
                metrics = new NodeResultMetricsEvent
                {
                    latency_nanoseconds = 1000900000,
                    jitter_nanoseconds = 0,
                    http_probe_failure_percent = 0,
                    download_bytes_per_second = 5D * 1024D * 1024D - 1D,
                    download_tested = true,
                    download_complete = true
                }
            };
            NodeResultProjection.Apply(node, boundary);
            Assert(node.State == "失败" && Math.Abs(node.LatencyMs - 1000.9D) < 0.0001D
                && node.DownloadMbps < 5D && node.DownloadTested && node.DownloadComplete,
                "v5 投影分开 usable、tested 和 complete，不从格式化 cells 推断");
        }

        private static void TestRegionProtocolValidation()
        {
            RegionProtocolValidator valid = new RegionProtocolValidator(new[] { "node-a", "node-b" });
            valid.AcceptProtocol(2);
            valid.AcceptRegionCount(2);
            valid.AcceptEvent(new NodeRegionEvent
            {
                node_id = "node-a", success = true, country_code = "JP",
                country = "日本", city = "东京", emoji = "🇯🇵", provider = "mock"
            });
            valid.AcceptEvent(new NodeRegionEvent
            {
                node_id = "node-b", success = false, error = "timeout"
            });
            valid.ValidateCompletion();
            Assert(valid.IsComplete && valid.ResultCount == 2, "完整地区协议通过");

            JavaScriptSerializer serializer = new JavaScriptSerializer();
            string failureLine = "@regionjson\t" + Convert.ToBase64String(Encoding.UTF8.GetBytes(
                "{\"node_id\":\"node-a\",\"success\":false,\"error\":\"timeout\"}"))
                .TrimEnd('=');
            RegionProtocolValidator parsed = new RegionProtocolValidator(new[] { "node-a" });
            RegionProtocolLineParser.AcceptLine("@protocol\t2", parsed, serializer);
            RegionProtocolLineParser.AcceptLine("@regions\t1", parsed, serializer);
            NodeRegionEvent parsedEvent =
                RegionProtocolLineParser.AcceptLine(failureLine, parsed, serializer);
            parsed.ValidateCompletion();
            Assert(parsedEvent != null && !parsedEvent.success && parsedEvent.error == "timeout",
                "地区协议文本行完整解析");

            AssertInvalidOperation(delegate
            {
                RegionProtocolLineParser.AcceptLine(failureLine,
                    new RegionProtocolValidator(new[] { "node-a" }), serializer);
            }, "地区协议拒绝事件早于表头");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator duplicateHeader =
                    new RegionProtocolValidator(new[] { "node-a" });
                RegionProtocolLineParser.AcceptLine("@protocol\t2", duplicateHeader, serializer);
                RegionProtocolLineParser.AcceptLine("@protocol\t2", duplicateHeader, serializer);
            }, "地区协议拒绝重复版本行");
            AssertInvalidOperation(delegate
            {
                RegionProtocolLineParser.AcceptLine("unexpected text",
                    new RegionProtocolValidator(new[] { "node-a" }), serializer);
            }, "地区协议拒绝普通文本输出");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator malformed =
                    new RegionProtocolValidator(new[] { "node-a" });
                RegionProtocolLineParser.AcceptLine("@protocol\t2", malformed, serializer);
                RegionProtocolLineParser.AcceptLine("@regions\t1", malformed, serializer);
                RegionProtocolLineParser.AcceptLine("@regionjson\t!!!", malformed, serializer);
            }, "地区协议拒绝畸形 Base64");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator invalidUtf8 =
                    new RegionProtocolValidator(new[] { "node-a" });
                RegionProtocolLineParser.AcceptLine("@protocol\t2", invalidUtf8, serializer);
                RegionProtocolLineParser.AcceptLine("@regions\t1", invalidUtf8, serializer);
                RegionProtocolLineParser.AcceptLine("@regionjson\t/w", invalidUtf8, serializer);
            }, "地区协议拒绝非法 UTF-8");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator malformedJson =
                    new RegionProtocolValidator(new[] { "node-a" });
                RegionProtocolLineParser.AcceptLine("@protocol\t2", malformedJson, serializer);
                RegionProtocolLineParser.AcceptLine("@regions\t1", malformedJson, serializer);
                string encoded = Convert.ToBase64String(Encoding.UTF8.GetBytes("{" )).TrimEnd('=');
                RegionProtocolLineParser.AcceptLine("@regionjson\t" + encoded,
                    malformedJson, serializer);
            }, "地区协议拒绝畸形 JSON");

            AssertInvalidOperation(delegate
            {
                new RegionProtocolValidator(new[] { "node-a" }).AcceptProtocol(3);
            }, "地区协议拒绝错误版本");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator mismatch = new RegionProtocolValidator(new[] { "node-a" });
                mismatch.AcceptProtocol(2);
                mismatch.AcceptRegionCount(2);
            }, "地区协议拒绝声明数量不匹配");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator unknown = new RegionProtocolValidator(new[] { "node-a" });
                unknown.AcceptProtocol(2);
                unknown.AcceptRegionCount(1);
                unknown.AcceptEvent(new NodeRegionEvent
                {
                    node_id = "other", success = false, error = "unknown"
                });
            }, "地区协议拒绝未知节点 ID");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator duplicate = new RegionProtocolValidator(new[] { "node-a" });
                duplicate.AcceptProtocol(2);
                duplicate.AcceptRegionCount(1);
                NodeRegionEvent result = new NodeRegionEvent
                {
                    node_id = "node-a", success = false, error = "timeout"
                };
                duplicate.AcceptEvent(result);
                duplicate.AcceptEvent(result);
            }, "地区协议拒绝重复结果");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator incomplete =
                    new RegionProtocolValidator(new[] { "node-a", "node-b" });
                incomplete.AcceptProtocol(2);
                incomplete.AcceptRegionCount(2);
                incomplete.AcceptEvent(new NodeRegionEvent
                {
                    node_id = "node-a", success = false, error = "timeout"
                });
                incomplete.ValidateCompletion();
            }, "地区协议拒绝缺失结果的成功终态");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator.ValidateEventEnvelope(new Dictionary<string, object>
                {
                    { "node_id", "node-a" }, { "success", "true" }
                });
            }, "地区协议拒绝错误的原始字段类型");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator.ValidateEventEnvelope(new Dictionary<string, object>
                {
                    { "node_id", "node-a" }, { "success", false }, { "error", "bad" },
                    { "extra", "unexpected" }
                });
            }, "地区协议拒绝未知字段");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator missingProvider =
                    new RegionProtocolValidator(new[] { "node-a" });
                missingProvider.AcceptProtocol(2);
                missingProvider.AcceptRegionCount(1);
                missingProvider.AcceptEvent(new NodeRegionEvent
                {
                    node_id = "node-a", success = true, country_code = "JP", country = "日本"
                });
            }, "地区协议拒绝成功事件缺少提供商");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator conflictingFailure =
                    new RegionProtocolValidator(new[] { "node-a" });
                conflictingFailure.AcceptProtocol(2);
                conflictingFailure.AcceptRegionCount(1);
                conflictingFailure.AcceptEvent(new NodeRegionEvent
                {
                    node_id = "node-a", success = false, country_code = "JP", error = "timeout"
                });
            }, "地区协议拒绝失败事件携带成功字段");
            AssertInvalidOperation(delegate
            {
                RegionProtocolValidator newlineCode =
                    new RegionProtocolValidator(new[] { "node-a" });
                newlineCode.AcceptProtocol(2);
                newlineCode.AcceptRegionCount(1);
                newlineCode.AcceptEvent(new NodeRegionEvent
                {
                    node_id = "node-a", success = true, country_code = "JP\n",
                    country = "日本", provider = "mock"
                });
            }, "地区协议拒绝国家代码尾随换行");

            NodeSnapshot original = new NodeSnapshot
            {
                RegionState = "成功", RegionCountryCode = "HK", RegionCountry = "香港",
                RegionCity = "九龙", RegionEmoji = "🇭🇰", RegionError = "old"
            };
            NodeRegionSnapshot snapshot = NodeRegionSnapshot.Capture(original);
            RegionEventProjection.Apply(original, new NodeRegionEvent
            {
                node_id = "node-a", success = false, error = "timeout"
            });
            Assert(original.RegionState == "查询失败" && original.RegionCountryCode == ""
                && original.RegionError == "timeout", "失败地区事件投影并保留原因");
            snapshot.Restore(original);
            Assert(original.RegionState == "成功" && original.RegionCountryCode == "HK"
                && original.RegionCountry == "香港" && original.RegionCity == "九龙"
                && original.RegionEmoji == "🇭🇰" && original.RegionError == "old",
                "地区查询回滚恢复完整旧状态");
        }

        private static void TestNodeFeatures()
        {
            Assert(NodeListPresentation.Headers.SequenceEqual(new[]
            {
                "序号", "节点名称", "类型", "HTTP 延迟", "下载速度", "出口地区", "状态"
            }), "固定七列");
            Assert(NodeListPresentation.MetricText(false, "12MB/s") == "未测试", "未测试占位值");
            Assert(NodeListPresentation.TransferMetricText(true, false, "2.00 MB/s")
                == "2.00 MB/s", "未完成传输仍显示已传输部分的采样速度");
            Assert(NodeListPresentation.StatusText(new NodeSnapshot
            {
                State = "失败", DownloadTested = true, DownloadComplete = false
            }) == "失败（传输未完成）", "未完成传输状态显式区分");
            object[] protocolOptions = NodeListPresentation.ProtocolFilterOptions(new[]
            {
                new NodeSnapshot { Type = "ss" }, new NodeSnapshot { Type = "ssr" },
                new NodeSnapshot { Type = "vmess" }, new NodeSnapshot { Type = "vless" },
                new NodeSnapshot { Type = "trojan" }, new NodeSnapshot { Type = "hysteria" },
                new NodeSnapshot { Type = "hysteria2" }, new NodeSnapshot { Type = "tuic" },
                new NodeSnapshot { Type = "anytls" }, new NodeSnapshot { Type = "http" },
                new NodeSnapshot { Type = "socks5" }
            });
            Assert(protocolOptions.Length == 12
                && protocolOptions.Cast<object>().Any(delegate(object value)
                    { return string.Equals(Convert.ToString(value), "SSR", StringComparison.Ordinal); })
                && protocolOptions.Cast<object>().Any(delegate(object value)
                    { return string.Equals(Convert.ToString(value), "TUIC", StringComparison.Ordinal); })
                && protocolOptions.Cast<object>().Any(delegate(object value)
                    { return string.Equals(Convert.ToString(value), "AnyTLS", StringComparison.Ordinal); })
                && protocolOptions.Cast<object>().Any(delegate(object value)
                    { return string.Equals(Convert.ToString(value), "SOCKS5", StringComparison.Ordinal); }),
                "协议筛选从本轮节点类型动态生成");
            Assert(NodeListPresentation.RegionFilterOptions(null).SequenceEqual(new object[] { "全部" }),
                "未查询出口地区时不显示无效的固定地区选项");
            object[] regionOptions = NodeListPresentation.RegionFilterOptions(new[]
            {
                new NodeSnapshot
                    { RegionState = "成功", RegionCountryCode = "DE", RegionCountry = "德国" },
                new NodeSnapshot
                    { RegionState = "成功", RegionCountryCode = "BR", RegionCountry = "巴西" },
                new NodeSnapshot
                    { RegionState = "查询失败", RegionCountryCode = "JP", RegionCountry = "日本" },
                new NodeSnapshot
                    { RegionState = "未查询", RegionCountryCode = "US", RegionCountry = "美国" }
            });
            Assert(regionOptions.SequenceEqual(new object[] { "全部", "BR 巴西", "DE 德国" }),
                "地区筛选只按本轮查询成功结果动态生成且不限于预设国家");
            int visibleIndex = 0;
            Assert(NodeListPresentation.NextVisibleIndex(ref visibleIndex) == "1"
                && NodeListPresentation.NextVisibleIndex(ref visibleIndex) == "2", "动态序号");
            Assert(StatusStatistics.Format(200, 36, 8, 29, 13, 158)
                == "总数 200 | 筛选后 36 | 已选 8 | 有效 29 | 失败 13 | 等待 158",
                "状态栏统计");
            using (DataGridView selectionGrid = new DataGridView
            {
                AllowUserToAddRows = false,
                MultiSelect = true,
                SelectionMode = DataGridViewSelectionMode.FullRowSelect
            })
            {
                selectionGrid.Columns.Add("node", "node");
                selectionGrid.Rows.Add("visible-a");
                selectionGrid.Rows.Add("visible-b");
                selectionGrid.Rows.Add("hidden-c");
                selectionGrid.CurrentCell = null;
                selectionGrid.Rows[2].Visible = false;
                selectionGrid.SelectAll();
                Assert(NodeListSelection.GetSelectedVisibleRows(selectionGrid).Count == 2,
                    "列表操作忽略被原生全选纳入的隐藏行");
                NodeListSelection.SelectAllVisibleRows(selectionGrid);
                Assert(selectionGrid.SelectedRows.Count == 2
                    && NodeListSelection.GetSelectedVisibleRows(selectionGrid).Count == 2,
                    "筛选后的 Ctrl+A 只选中可见行");
            }
            Assert(NodeNamePolicy.Normalize("  renamed-node  ") == "renamed-node", "节点名称规范化");
            bool invalidNameRejected = false;
            try
            {
                NodeNamePolicy.Normalize("bad\nname");
            }
            catch (InvalidOperationException)
            {
                invalidNameRejected = true;
            }
            Assert(invalidNameRejected, "节点名称控制字符校验");
            JavaScriptSerializer settingsSerializer = new JavaScriptSerializer();
            AppSettings migratedSettings = settingsSerializer.Deserialize<AppSettings>(
                "{\"OutputPath\":\"filtered.yaml\",\"NodeNotes\":{\"stable-id\":\"old\"}}");
            Assert(!settingsSerializer.Serialize(migratedSettings).Contains("NodeNotes"), "旧备注设置清除");

            NodeSnapshot valid = new NodeSnapshot
            {
                Name = "valid",
                Type = "VLESS",
                State = "有效",
                LatencyMs = 80,
                DownloadTested = true,
                DownloadComplete = true,
                DownloadMbps = 10,
                RegionState = "成功",
                RegionCountryCode = "HK",
                RegionCountry = "香港",
                RegionCity = "香港",
                RegionEmoji = "🇭🇰",
                Config = new Dictionary<string, object>
                {
                    { "name", "valid" },
                    { "type", "vless" },
                    { "ws-opts", new Dictionary<string, object>
                        {
                            { "path", "/socket" },
                            { "headers", new Dictionary<string, object> { { "Host", "cdn.example.com" } } }
                        }
                    }
                }
            };
            NodeSnapshot failed = new NodeSnapshot { Name = "failed", State = "失败", LatencyMs = 20 };
            DataGridViewRow validRow = new DataGridViewRow { Tag = valid };
            DataGridViewRow failedRow = new DataGridViewRow { Tag = failed };
            NodeRowComparer comparer = new NodeRowComparer("", ListSortDirection.Ascending, true);
            Assert(comparer.Compare(validRow, failedRow) < 0, "默认排序有效节点优先");
            Assert(RegionFormatter.Format(valid) == "🇭🇰 香港", "国家城市相同时只显示一次");
            valid.RegionCity = "九龙";
            Assert(RegionFormatter.Format(valid) == "🇭🇰 香港·九龙", "出口地区格式化");
            Assert(RegionFormatter.Ellipsize("🇯🇵 日本·一个非常非常长的城市名称", 10).EndsWith("…"),
                "出口地区过长省略");

            NodeSnapshot untested = new NodeSnapshot { Name = "untested", State = "有效" };
            DataGridViewRow untestedRow = new DataGridViewRow { Tag = untested };
            Assert(new NodeRowComparer("下载速度", ListSortDirection.Ascending, false)
                .Compare(validRow, untestedRow) < 0, "升序未测试置后");
            Assert(new NodeRowComparer("下载速度", ListSortDirection.Descending, false)
                .Compare(validRow, untestedRow) < 0, "降序未测试仍置后");

            NodeListFilterCriteria combinedFilter = new NodeListFilterCriteria
            {
                Status = "有效",
                MaxLatencyExclusive = 100,
                Protocol = "vless",
                RegionCountryCode = "HK"
            };
            Assert(NodeListFilter.Matches(valid, combinedFilter), "四类列表筛选组合匹配");
            valid.LatencyMs = 100;
            Assert(!NodeListFilter.Matches(valid, combinedFilter), "延迟筛选使用严格小于");
            valid.LatencyMs = 80;
            Assert(!NodeListFilter.Matches(new NodeSnapshot
                { State = "等待", LatencyMs = 20, Type = "vless", RegionCountryCode = "HK" },
                new NodeListFilterCriteria { Status = "失败" }), "失败筛选排除等待节点");
            Assert(NodeListFilter.Matches(new NodeSnapshot
                { State = "有效", LatencyMs = 20, Type = "shadowsocks", RegionCountryCode = "JP" },
                new NodeListFilterCriteria { Protocol = "SS" }), "SS 协议别名筛选");
            Assert(!NodeListFilter.Matches(new NodeSnapshot
                { State = "有效", RegionCountryCode = "", Name = "JP guessed" },
                new NodeListFilterCriteria { RegionCountryCode = "JP" }), "未查询节点不按名称冒充地区");
            Assert(!NodeListFilter.Matches(new NodeSnapshot
                { State = "有效", RegionState = "未查询", RegionCountryCode = "JP" },
                new NodeListFilterCriteria { RegionCountryCode = "JP" }),
                "非成功状态不使用残留地区代码筛选");

            string meta = NodeMetaFormatter.Format(new[] { valid });
            Assert(meta.StartsWith("proxies:", StringComparison.Ordinal), "Clash Meta 根节点");
            Assert(meta.Contains("\"ws-opts\"") && meta.Contains("\"Host\":\"cdn.example.com\""),
                "Clash Meta 保留嵌套参数");
        }

        private static void TestSettingsStoreOverride()
        {
            string previous = Environment.GetEnvironmentVariable(
                SettingsStore.SettingsDirectoryEnvironmentVariable);
            string directory = Path.Combine(Path.GetTempPath(),
                "ClashSpeedTestGUI-SettingsTest-" + Guid.NewGuid().ToString("N"));
            try
            {
                Environment.SetEnvironmentVariable(
                    SettingsStore.SettingsDirectoryEnvironmentVariable, directory);
                Assert(string.Equals(SettingsStore.SettingsPath,
                    Path.Combine(directory, "settings.json"), StringComparison.OrdinalIgnoreCase),
                    "UI 测试设置目录可隔离");
                AppSettings value = AppSettings.CreateDefault();
                value.ConfigSource = "fixture.yaml";
                SettingsStore.Save(value);
                Assert(SettingsStore.Load().ConfigSource == "fixture.yaml",
                    "隔离设置可以保存并读取");
                File.WriteAllText(SettingsStore.SettingsPath,
                    "{\"SpeedMode\":\"full\",\"UploadSizeMb\":50,"
                    + "\"MinUploadSpeed\":2,\"TimeoutSeconds\":17,\"MaxPacketLoss\":25}",
                    new UTF8Encoding(false));
                AppSettings migrated = SettingsStore.Load();
                string rewritten = File.ReadAllText(SettingsStore.SettingsPath, Encoding.UTF8);
                Assert(migrated.SpeedMode == "download"
                    && migrated.TimeoutSeconds == 17
                    && migrated.ProbeTimeoutSeconds == 3
                    && migrated.MaxHTTPProbeFailure == 25,
                    "旧 full、上传字段、下载超时和探测失败阈值安全迁移");
                Assert(!rewritten.Contains("UploadSizeMb")
                    && !rewritten.Contains("MinUploadSpeed")
                    && !rewritten.Contains("MaxPacketLoss")
                    && rewritten.Contains("ProbeTimeoutSeconds"),
                    "保存迁移设置时清除旧上传字段并写入探测超时");
            }
            finally
            {
                Environment.SetEnvironmentVariable(
                    SettingsStore.SettingsDirectoryEnvironmentVariable, previous);
                if (Directory.Exists(directory)) Directory.Delete(directory, true);
            }
        }

        private static void TestAtomicFileCommit()
        {
            string directory = Path.Combine(Path.GetTempPath(), "ClashSpeedTestGUI-SelfTest-"
                + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(directory);
            try
            {
                string destination = Path.Combine(directory, "output.yaml");
                string temporary = Path.Combine(directory, "output.tmp.yaml");
                File.WriteAllText(destination, "old", Encoding.UTF8);
                File.WriteAllText(temporary, "new", Encoding.UTF8);
                AtomicFile.Commit(temporary, destination);
                Assert(File.ReadAllText(destination, Encoding.UTF8) == "new", "原子替换输出");
                Assert(!File.Exists(temporary), "原子替换后清理临时文件");
            }
            finally
            {
                if (Directory.Exists(directory)) Directory.Delete(directory, true);
            }
        }

        private static void TestOutputPathPolicy()
        {
            string directory = Path.Combine(Path.GetTempPath(), "ClashSpeedTestGUI-PathPolicy");
            string input = Path.Combine(directory, "input.yaml");
            string output = Path.Combine(directory, "output.yaml");
            OutputPathPolicy.EnsureSafe(output, new[] { input }, new string[0]);

            bool inputRejected = false;
            try
            {
                OutputPathPolicy.EnsureSafe(input.ToUpperInvariant(), new[] { input }, new string[0]);
            }
            catch (InvalidOperationException)
            {
                inputRejected = true;
            }
            Assert(inputRejected, "输出不得覆盖输入配置");

            bool executableRejected = false;
            try
            {
                OutputPathPolicy.EnsureSafe(input, new string[0], new[] { input });
            }
            catch (InvalidOperationException)
            {
                executableRejected = true;
            }
            Assert(executableRejected, "输出不得覆盖程序文件");
        }

        private static void TestGistSelection()
        {
            object[] gists = new object[]
            {
                new Dictionary<string, object>
                {
                    {"id", "other"},
                    {"description", "Other"}
                },
                new Dictionary<string, object>
                {
                    {"id", "dedicated"},
                    {"html_url", "https://gist.github.com/user/dedicated"},
                    {"description", "Clash-SpeedTest GUI: existing.yaml"}
                }
            };

            Dictionary<string, object> dedicated = gists[1] as Dictionary<string, object>;
            dedicated["files"] = new Dictionary<string, object>
            {
                {"existing.yaml", new Dictionary<string, object>()}
            };
            GistInfo gist = GistClient.FindDedicatedGistInList(gists, "existing.yaml");
            Assert(gist != null && gist.Id == "dedicated"
                && gist.HtmlUrl == "https://gist.github.com/user/dedicated"
                && gist.FileExists,
                "按文件名匹配独立 Gist");
            Assert(GistClient.FindDedicatedGistInList(gists, "new.yaml") == null,
                "不同文件名不复用已有 Gist ID");
            Assert(GistClient.FindDedicatedGistInList(new object[0], "new.yaml") == null,
                "无专用 Gist 时返回空");
            Assert(GistClient.BuildGistDescription("existing.yaml")
                == "Clash-SpeedTest GUI: existing.yaml",
                "文件名生成独立 Gist 描述");
            Assert(GistClient.BuildStableRawUrl("user", "dedicated", "existing.yaml")
                == "https://gist.githubusercontent.com/user/dedicated/raw/existing.yaml",
                "同名文件生成稳定 raw 订阅链接");
            Assert(GistClient.BuildStableRawUrl("user", "dedicated", "香港 节点.yaml")
                == "https://gist.githubusercontent.com/user/dedicated/raw/"
                    + "%E9%A6%99%E6%B8%AF%20%E8%8A%82%E7%82%B9.yaml",
                "中文和空格文件名正确编码");
        }

        private static void TestGistCancellation()
        {
            using (CancellationTokenSource cancellation = new CancellationTokenSource())
            {
                cancellation.Cancel();
                AssertOperationCanceled(delegate
                {
                    GistClient.CreateOrUpdate("", "invalid user",
                        Path.Combine(Path.GetTempPath(), "missing-gist-file.yaml"),
                        cancellation.Token);
                }, "Gist 在读取文件或发起网络请求前响应取消");
            }

            string directory = Path.Combine(Path.GetTempPath(), "ClashSpeedTestGUI-GistCancel-"
                + Guid.NewGuid().ToString("N"));
            string filePath = Path.Combine(directory, "result.yaml");
            Directory.CreateDirectory(directory);
            File.WriteAllText(filePath, "proxies: []", new UTF8Encoding(false));
            try
            {
                BlockingGistHandler handler = new BlockingGistHandler();
                using (CancellationTokenSource cancellation = new CancellationTokenSource())
                {
                    Exception observed = null;
                    Task request = Task.Run(delegate
                    {
                        try
                        {
                            GistClient.CreateOrUpdate("test-token", "tester", filePath,
                                cancellation.Token, handler);
                        }
                        catch (Exception ex)
                        {
                            observed = ex;
                        }
                    });
                    try
                    {
                        Assert(handler.BlockingRequestStarted.Wait(3000),
                            "Gist 测试进入运行中的列表请求");
                        cancellation.Cancel();
                        Assert(request.Wait(3000), "运行中的 Gist 请求在取消后及时退出");
                        Assert(observed is OperationCanceledException && handler.RequestCount == 2,
                            "Gist 运行中取消不发起后续创建或更新请求");
                    }
                    finally
                    {
                        if (!cancellation.IsCancellationRequested) cancellation.Cancel();
                        if (!request.IsCompleted) request.Wait(3000);
                    }
                }

                IndependentlyCanceledGistHandler timeoutHandler =
                    new IndependentlyCanceledGistHandler();
                bool timeoutReported = false;
                try
                {
                    GistClient.CreateOrUpdate("test-token", "tester", filePath,
                        CancellationToken.None, timeoutHandler);
                }
                catch (TimeoutException)
                {
                    timeoutReported = true;
                }
                Assert(timeoutReported && timeoutHandler.RequestCount == 1,
                    "非调用方取消按 Gist 超时报告且不继续请求");
            }
            finally
            {
                if (Directory.Exists(directory)) Directory.Delete(directory, true);
            }
        }
    }
}

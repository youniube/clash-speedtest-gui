using System;
using System.Collections;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text;
using System.Threading;
using System.Web.Script.Serialization;

namespace ClashSpeedTestGUI.UiFixtures
{
    internal static class FixtureTool
    {
        private const string FixtureRootEnvironment =
            "CLASH_SPEEDTEST_GUI_UI_FIXTURE_ROOT";
        private const int SpeedProtocolVersion = 5;
        private const int RegionProtocolVersion = 2;
        private const int SourcePreparationProtocolVersion = 1;

        private static readonly UTF8Encoding Utf8 = new UTF8Encoding(false, true);
        private static readonly JavaScriptSerializer Json = new JavaScriptSerializer
        {
            MaxJsonLength = int.MaxValue
        };

        private static readonly NodeDefinition NodeA = new NodeDefinition
        {
            Id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            Name = "Fixture 香港 A",
            Type = "ss",
            ShareUrl = "ss://YWVzLTEyOC1nY206Zml4dHVyZS1h@127.0.0.1:10001#Fixture%20A",
            Config = Dictionary(
                "name", "Fixture 香港 A",
                "type", "ss",
                "server", "127.0.0.1",
                "port", 10001,
                "cipher", "aes-128-gcm",
                "password", "fixture-a",
                "udp", true)
        };

        private static readonly NodeDefinition NodeB = new NodeDefinition
        {
            Id = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            Name = "Fixture 美国 B",
            Type = "trojan",
            ShareUrl = "trojan://fixture-b@127.0.0.1:10002?sni=fixture.invalid#Fixture%20B",
            Config = Dictionary(
                "name", "Fixture 美国 B",
                "type", "trojan",
                "server", "127.0.0.1",
                "port", 10002,
                "password", "fixture-b",
                "sni", "fixture.invalid",
                "skip-cert-verify", true,
                "udp", true)
        };

        private static readonly NodeDefinition NodeC = new NodeDefinition
        {
            Id = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
            Name = "Fixture 失败 C",
            Type = "vless",
            ShareUrl = "vless://00000000-0000-4000-8000-000000000003@127.0.0.1:10003?type=tcp#Fixture%20C",
            Config = Dictionary(
                "name", "Fixture 失败 C",
                "type", "vless",
                "server", "127.0.0.1",
                "port", 10003,
                "uuid", "00000000-0000-4000-8000-000000000003",
                "network", "tcp",
                "tls", false,
                "udp", true)
        };

        private static readonly NodeDefinition[] AllNodes = { NodeA, NodeB, NodeC };
        private static readonly NodeDefinition[] ExportedNodes = { NodeA, NodeB };

        private static int Main(string[] args)
        {
            using (StreamWriter output = NewStandardWriter(Console.OpenStandardOutput()))
            using (StreamWriter error = NewStandardWriter(Console.OpenStandardError()))
            {
                try
                {
                    string role = ResolveRole(ref args);
                    if (string.Equals(role, "parser", StringComparison.Ordinal))
                        return RunParser(args, output);
                    if (string.Equals(role, "runner", StringComparison.Ordinal))
                        return RunRunner(args, output, error);
                    throw new InvalidOperationException(
                        "将夹具复制为 subscription-parser.exe 或 speedtest-runner.exe，"
                        + "或者使用 --role parser|runner。");
                }
                catch (Exception ex)
                {
                    error.WriteLine("UI fixture error: " + ex.Message);
                    return 2;
                }
            }
        }

        private static string ResolveRole(ref string[] args)
        {
            string executable = Path.GetFileNameWithoutExtension(
                Assembly.GetExecutingAssembly().Location).ToLowerInvariant();
            if (executable == "subscription-parser") return "parser";
            if (executable == "speedtest-runner") return "runner";

            if (args.Length >= 2 && string.Equals(args[0], "--role", StringComparison.Ordinal))
            {
                string role = (args[1] ?? "").Trim().ToLowerInvariant();
                string[] remaining = new string[args.Length - 2];
                Array.Copy(args, 2, remaining, 0, remaining.Length);
                args = remaining;
                return role;
            }
            return "";
        }

        private static int RunParser(string[] rawArguments, StreamWriter output)
        {
            FixtureArguments arguments = FixtureArguments.Parse(rawArguments);
            string inputPath = RequireSandboxFile(arguments.Require("-input"), "解析输入");
            if (!File.Exists(inputPath))
                throw new FileNotFoundException("解析输入不存在。", inputPath);

            string outputPath = RequireSandboxPath(arguments.Require("-output"), "解析输出");
            EnsureParentDirectory(outputPath);
            File.WriteAllText(outputPath, BuildYaml(AllNodes), Utf8);
            WriteSignal("parser-completed.json", "parser", "success",
                "input=" + Path.GetFileName(inputPath) + "; nodes=3");
            output.WriteLine("fixture parser wrote 3 nodes");
            return 0;
        }

        private static int RunRunner(
            string[] rawArguments, StreamWriter output, StreamWriter error)
        {
            FixtureArguments arguments = FixtureArguments.Parse(rawArguments);
            if (arguments.Has("-prepare-sources"))
                return RunPrepareSources(arguments, output);
            if (arguments.Has("-list-config"))
                return RunListConfig(arguments, output);
            if (arguments.Has("-region-query"))
                return RunRegionQuery(arguments, output, error);
            if (arguments.Has("-manage-config"))
                return RunManageConfig(arguments, output);
            return RunSpeedTest(arguments, output);
        }

        private static int RunPrepareSources(FixtureArguments arguments, StreamWriter output)
        {
            string requestPath = RequireSandboxFile(
                arguments.Require("-prepare-sources"), "Provider 预备请求");
            if (!File.Exists(requestPath))
                throw new FileNotFoundException("Provider 预备请求不存在。", requestPath);

            Dictionary<string, object> envelope = Json.DeserializeObject(
                File.ReadAllText(requestPath, Utf8)) as Dictionary<string, object>;
            if (envelope == null || envelope.Count != 2
                || !envelope.ContainsKey("version") || !envelope.ContainsKey("sources")
                || !(envelope["version"] is int)
                || (int)envelope["version"] != SourcePreparationProtocolVersion)
                throw new InvalidOperationException("Provider 预备请求协议无效。");

            object[] rawSources = envelope["sources"] as object[];
            if (rawSources == null || rawSources.Length == 0)
                throw new InvalidOperationException("Provider 预备请求必须包含来源。");

            string directory = Path.GetDirectoryName(requestPath);
            List<string> prepared = new List<string>();
            List<string> dependencies = new List<string>();
            for (int index = 0; index < rawSources.Length; index++)
            {
                Dictionary<string, object> source = rawSources[index]
                    as Dictionary<string, object>;
                if (source == null || source.Count != 3
                    || !source.ContainsKey("path") || !source.ContainsKey("origin")
                    || !source.ContainsKey("local_dependency"))
                    throw new InvalidOperationException("Provider 预备来源字段无效。");

                string inputPath = RequireSandboxFile(source["path"] as string,
                    "Provider 预备输入");
                if (!File.Exists(inputPath))
                    throw new FileNotFoundException("Provider 预备输入不存在。", inputPath);
                string origin = source["origin"] as string;
                string localDependency = source["local_dependency"] as string;
                if (string.Equals(origin, "local", StringComparison.Ordinal))
                {
                    localDependency = RequireSandboxFile(localDependency, "Provider 本地依赖");
                    if (!File.Exists(localDependency))
                        throw new FileNotFoundException("Provider 本地依赖不存在。", localDependency);
                    dependencies.Add(localDependency);
                }
                else if (!string.Equals(origin, "remote", StringComparison.Ordinal)
                    && !string.Equals(origin, "inline", StringComparison.Ordinal))
                {
                    throw new InvalidOperationException("Provider 预备来源类型无效。");
                }
                else if (!string.IsNullOrWhiteSpace(localDependency))
                {
                    throw new InvalidOperationException("非本地来源不能声明本地依赖。");
                }

                string preparedPath = Path.Combine(directory, string.Format(
                    CultureInfo.InvariantCulture, "materialized-fixture-{0:D3}.yaml", index + 1));
                File.Copy(inputPath, preparedPath, false);
                prepared.Add(preparedPath);
            }

            output.WriteLine(Json.Serialize(Dictionary(
                "version", SourcePreparationProtocolVersion,
                "config_paths", prepared.ToArray(),
                "local_dependencies", dependencies.Distinct(
                    StringComparer.OrdinalIgnoreCase).ToArray())));
            WriteSignal("prepare-sources-completed.json", "runner", "prepare-sources",
                "sources=" + prepared.Count.ToString(CultureInfo.InvariantCulture)
                    + "; dependencies=" + dependencies.Count.ToString(CultureInfo.InvariantCulture));
            return 0;
        }

        private static int RunListConfig(FixtureArguments arguments, StreamWriter output)
        {
            string configPath = RequireSandboxFile(
                arguments.Require("-list-config"), "节点清单配置");
            if (!File.Exists(configPath))
                throw new FileNotFoundException("节点清单配置不存在。", configPath);

            NodeDefinition[] configuredNodes = LoadManagedNodes(configPath);
            object[] nodes = new object[configuredNodes.Length];
            for (int index = 0; index < configuredNodes.Length; index++)
                nodes[index] = configuredNodes[index].ToManifest();
            output.WriteLine(Json.Serialize(Dictionary("nodes", nodes)));
            WriteSignal("list-config-completed.json", "runner", "list-config",
                "nodes=" + nodes.Length.ToString(CultureInfo.InvariantCulture)
                + "; input=" + Path.GetFileName(configPath));
            return 0;
        }

        private static int RunManageConfig(FixtureArguments arguments, StreamWriter output)
        {
            string requestPath = RequireSandboxFile(
                arguments.Require("-manage-config"), "节点管理请求");
            string inputPath = RequireSandboxFile(arguments.Require("-c"), "节点管理输入");
            string outputPath = RequireSandboxPath(
                arguments.Require("-output"), "节点管理输出");
            if (!File.Exists(requestPath))
                throw new FileNotFoundException("节点管理请求不存在。", requestPath);
            if (!File.Exists(inputPath))
                throw new FileNotFoundException("节点管理输入不存在。", inputPath);

            Dictionary<string, object> envelope = Json.DeserializeObject(
                File.ReadAllText(requestPath, Utf8)) as Dictionary<string, object>;
            if (envelope == null || envelope.Count != 2
                || !envelope.ContainsKey("renames") || !envelope.ContainsKey("deletes"))
                throw new InvalidOperationException("节点管理请求必须只包含 renames 和 deletes。");

            Dictionary<string, string> renames = ReadRenames(envelope["renames"]);
            HashSet<string> deletes = ReadDeletes(envelope["deletes"]);
            foreach (string id in renames.Keys)
            {
                if (deletes.Contains(id))
                    throw new InvalidOperationException("同一节点不能同时重命名和删除：" + id);
            }

            NodeDefinition[] current = LoadManagedNodes(inputPath);
            HashSet<string> known = new HashSet<string>(
                current.Select(delegate(NodeDefinition node) { return node.Id; }),
                StringComparer.Ordinal);
            foreach (string id in renames.Keys.Concat(deletes))
            {
                if (!known.Contains(id))
                    throw new InvalidOperationException("节点管理请求包含未知节点 ID：" + id);
            }

            List<NodeDefinition> updated = new List<NodeDefinition>();
            int renamed = 0;
            int deleted = 0;
            foreach (NodeDefinition node in current)
            {
                if (deletes.Contains(node.Id))
                {
                    deleted++;
                    continue;
                }
                NodeDefinition updatedNode = node;
                string name;
                if (renames.TryGetValue(node.Id, out name))
                {
                    updatedNode = node.WithName(name);
                    renamed++;
                }
                updated.Add(updatedNode);
            }

            EnsureParentDirectory(outputPath);
            File.WriteAllText(outputPath, BuildYaml(updated), Utf8);
            object[] manifests = updated.Select(
                delegate(NodeDefinition node) { return node.ToManifest(); }).ToArray();
            output.WriteLine(Json.Serialize(Dictionary(
                "renamed", renamed,
                "deleted", deleted,
                "nodes", manifests)));
            WriteSignal("manage-config-completed.json", "runner", "manage-config",
                "renamed=" + renamed.ToString(CultureInfo.InvariantCulture)
                + "; deleted=" + deleted.ToString(CultureInfo.InvariantCulture)
                + "; nodes=" + updated.Count.ToString(CultureInfo.InvariantCulture));
            return 0;
        }

        private static int RunSpeedTest(FixtureArguments arguments, StreamWriter output)
        {
            ValidateRunnerConfigs(arguments.Require("-c"));
            string outputPath = RequireSandboxPath(arguments.Require("-output"), "测速输出");
            string speedMode = (arguments.Get("-speed-mode", "download") ?? "")
                .Trim().ToLowerInvariant();
            if (speedMode != "fast" && speedMode != "download")
                throw new InvalidOperationException("不支持的测速模式：" + speedMode);

            string scenario = ReadScenario("speed-mode.txt", "success",
                new[] { "success", "gated-success", "block-after-manifest" });
            string[] headers = SpeedHeaders(speedMode);
            output.WriteLine("@protocol\t" + SpeedProtocolVersion.ToString(CultureInfo.InvariantCulture));
            output.WriteLine(string.Join("\t", headers));
            output.WriteLine("@nodes\t" + AllNodes.Length.ToString(CultureInfo.InvariantCulture));
            foreach (NodeDefinition node in AllNodes)
                WriteEvent(output, "@nodejson", node.ToManifest());
            output.Flush();
            WriteSignal("speed-started.json", "runner", scenario,
                "manifests=3; mode=" + speedMode);

            if (scenario == "gated-success")
                WaitForControlFile("speed-release.signal");
            else if (scenario == "block-after-manifest")
                BlockForever();

            if (speedMode == "fast")
            {
                WriteSpeedProgress(output, NodeA, "probe_completed");
                WriteEvent(output, "@resultjson", BuildSpeedResult(NodeA, 1, speedMode, true));
                output.Flush();
                WriteSpeedProgress(output, NodeB, "probe_completed");
                WriteEvent(output, "@resultjson", BuildSpeedResult(NodeB, 2, speedMode, true));
                output.Flush();
                WriteSpeedProgress(output, NodeC, "probe_completed");
                WriteEvent(output, "@resultjson", BuildSpeedResult(NodeC, 3, speedMode, false));
            }
            else
            {
                WriteSpeedProgress(output, NodeA, "probe_completed");
                WriteSpeedProgress(output, NodeB, "probe_completed");
                WriteSpeedProgress(output, NodeC, "probe_completed");
                WriteEvent(output, "@resultjson", BuildSpeedResult(NodeC, 3, speedMode, false));
                output.Flush();
                WriteSpeedProgress(output, NodeA, "download_started");
                WriteEvent(output, "@resultjson", BuildSpeedResult(NodeA, 1, speedMode, true));
                output.Flush();
                WriteSpeedProgress(output, NodeB, "download_started");
                WriteEvent(output, "@resultjson", BuildSpeedResult(NodeB, 2, speedMode, true));
            }
            output.Flush();

            EnsureParentDirectory(outputPath);
            File.WriteAllText(outputPath, BuildYaml(ExportedNodes), Utf8);
            output.WriteLine("save config file to: " + outputPath);
            WriteSignal("speed-completed.json", "runner", scenario,
                "results=3; exported=2; mode=" + speedMode);
            return 0;
        }

        private static int RunRegionQuery(
            FixtureArguments arguments, StreamWriter output, StreamWriter error)
        {
            ValidateRunnerConfigs(arguments.Require("-c"));
            string requestPath = RequireSandboxFile(
                arguments.Require("-region-query"), "地区请求");
            string[] ids = ReadRegionRequest(requestPath);
            string scenario = ReadScenario("region-mode.txt", "all-success", new[]
            {
                "all-success", "mixed", "block-after-one", "malformed",
                "missing", "partial-nonzero"
            });

            output.WriteLine("@protocol\t" + RegionProtocolVersion.ToString(CultureInfo.InvariantCulture));
            output.WriteLine("@regions\t" + ids.Length.ToString(CultureInfo.InvariantCulture));
            output.Flush();
            WriteSignal("region-started.json", "runner", scenario,
                "requested=" + ids.Length.ToString(CultureInfo.InvariantCulture));

            if (scenario == "all-success")
            {
                WriteAllRegionEvents(output, ids, false);
                WriteSignal("region-completed.json", "runner", scenario,
                    "results=" + ids.Length.ToString(CultureInfo.InvariantCulture));
                return 0;
            }

            if (scenario == "mixed")
            {
                WriteAllRegionEvents(output, ids, true);
                WriteSignal("region-completed.json", "runner", scenario,
                    "results=" + ids.Length.ToString(CultureInfo.InvariantCulture));
                return 0;
            }

            if (scenario == "missing")
            {
                int count = Math.Max(0, ids.Length - 1);
                for (int index = 0; index < count; index++)
                {
                    WriteEvent(output, "@regionjson", BuildSuccessfulRegion(ids[index]));
                    if (index == 0) WriteRegionFirstEventSignal(scenario, ids[index]);
                }
                output.Flush();
                WriteSignal("region-completed.json", "runner", scenario,
                    "results=" + count.ToString(CultureInfo.InvariantCulture)
                    + "; intentionally-incomplete=true");
                return 0;
            }

            if (ids.Length == 0)
                throw new InvalidOperationException("地区请求必须至少包含一个节点 ID。");

            WriteEvent(output, "@regionjson", BuildSuccessfulRegion(ids[0]));
            output.Flush();
            WriteRegionFirstEventSignal(scenario, ids[0]);

            if (scenario == "block-after-one")
                BlockForever();

            if (scenario == "malformed")
            {
                output.WriteLine("@regionjson\t!!!");
                output.Flush();
                WriteSignal("region-completed.json", "runner", scenario,
                    "malformed-after=1");
                return 0;
            }

            if (scenario == "partial-nonzero")
            {
                error.WriteLine("fixture region query intentionally exited after one event");
                WriteSignal("region-completed.json", "runner", scenario,
                    "results=1; exit=7");
                return 7;
            }

            throw new InvalidOperationException("未处理的地区场景：" + scenario);
        }

        private static void WriteAllRegionEvents(
            StreamWriter output, IEnumerable<string> ids, bool mixed)
        {
            int index = 0;
            foreach (string id in ids)
            {
                object value = mixed && string.Equals(id, NodeB.Id, StringComparison.Ordinal)
                    ? BuildFailedRegion(id, "fixture region provider failure")
                    : BuildSuccessfulRegion(id);
                WriteEvent(output, "@regionjson", value);
                if (index == 0) WriteRegionFirstEventSignal(
                    mixed ? "mixed" : "all-success", id);
                index++;
            }
            output.Flush();
        }

        private static void WriteRegionFirstEventSignal(string scenario, string id)
        {
            WriteSignal("region-first-event.json", "runner", scenario,
                "node_id=" + id);
        }

        private static object BuildSuccessfulRegion(string id)
        {
            if (string.Equals(id, NodeA.Id, StringComparison.Ordinal))
            {
                return Dictionary(
                    "node_id", id,
                    "success", true,
                    "country_code", "HK",
                    "country", "香港",
                    "city", "香港",
                    "emoji", "🇭🇰",
                    "provider", "FixtureGeo",
                    "error", "");
            }
            if (string.Equals(id, NodeB.Id, StringComparison.Ordinal))
            {
                return Dictionary(
                    "node_id", id,
                    "success", true,
                    "country_code", "US",
                    "country", "美国",
                    "city", "洛杉矶",
                    "emoji", "🇺🇸",
                    "provider", "FixtureGeo",
                    "error", "");
            }
            if (string.Equals(id, NodeC.Id, StringComparison.Ordinal))
            {
                return Dictionary(
                    "node_id", id,
                    "success", true,
                    "country_code", "JP",
                    "country", "日本",
                    "city", "东京",
                    "emoji", "🇯🇵",
                    "provider", "FixtureGeo",
                    "error", "");
            }
            return BuildFailedRegion(id, "fixture does not know this node ID");
        }

        private static object BuildFailedRegion(string id, string message)
        {
            return Dictionary(
                "node_id", id,
                "success", false,
                "country_code", "",
                "country", "",
                "city", "",
                "emoji", "",
                "provider", "",
                "error", message);
        }

        private static object BuildSpeedResult(
            NodeDefinition node, int ordinal, string mode, bool usable)
        {
            long latency = node == NodeA ? 42000000L : node == NodeB ? 420000000L : 0L;
            long jitter = node == NodeA ? 2000000L : node == NodeB ? 12000000L : 0L;
            double probeFailure = node == NodeA ? 0D : node == NodeB ? 1.5D : 100D;
            bool downloadTested = usable && mode != "fast";
            bool downloadComplete = downloadTested;
            double downloadBytes = !downloadTested ? 0D
                : node == NodeA ? 8D * 1024D * 1024D : 12D * 1024D * 1024D;

            List<string> cells = new List<string>();
            cells.Add(ordinal.ToString(CultureInfo.InvariantCulture) + ".");
            cells.Add(node.Name);
            cells.Add(node.Type);
            cells.Add(usable
                ? (latency / 1000000L).ToString(CultureInfo.InvariantCulture) + "ms"
                : "未测试");
            if (mode != "fast")
            {
                cells.Add(usable
                    ? (jitter / 1000000L).ToString(CultureInfo.InvariantCulture) + "ms"
                    : "未测试");
                cells.Add(usable
                    ? probeFailure.ToString("0.##", CultureInfo.InvariantCulture) + "%"
                    : "100%");
                cells.Add(downloadTested
                    ? (downloadBytes / 1024D / 1024D).ToString("0.00", CultureInfo.InvariantCulture)
                        + " MB/s"
                    : "未测试");
            }
            object metrics = Dictionary(
                "latency_nanoseconds", latency,
                "jitter_nanoseconds", jitter,
                "http_probe_failure_percent", probeFailure,
                "download_bytes_per_second", downloadBytes,
                "download_tested", downloadTested,
                "download_complete", downloadComplete);
            return Dictionary(
                "id", node.Id,
                "cells", cells.ToArray(),
                "usable", usable,
                "metrics", metrics);
        }

        private static string[] SpeedHeaders(string mode)
        {
            if (mode == "fast")
                return new[] { "序号", "节点名称", "类型", "HTTP 延迟" };
            return new[]
            {
                "序号", "节点名称", "类型", "HTTP 延迟", "抖动", "HTTP 探测失败率", "下载速度"
            };
        }

        private static void WriteSpeedProgress(StreamWriter output, NodeDefinition node, string stage)
        {
            WriteEvent(output, "@progressjson", Dictionary("id", node.Id, "stage", stage));
        }

        private static string[] ReadRegionRequest(string requestPath)
        {
            if (!File.Exists(requestPath))
                throw new FileNotFoundException("地区请求不存在。", requestPath);
            object parsed = Json.DeserializeObject(File.ReadAllText(requestPath, Utf8));
            Dictionary<string, object> envelope = parsed as Dictionary<string, object>;
            object rawIds;
            if (envelope == null || envelope.Count != 1 || !envelope.TryGetValue("ids", out rawIds))
                throw new InvalidOperationException("地区请求必须只包含 ids 字段。");

            IEnumerable collection = rawIds as IEnumerable;
            if (collection == null || rawIds is string)
                throw new InvalidOperationException("地区请求 ids 必须是数组。");
            List<string> ids = new List<string>();
            HashSet<string> seen = new HashSet<string>(StringComparer.Ordinal);
            foreach (object item in collection)
            {
                string id = item as string;
                if (string.IsNullOrWhiteSpace(id)
                    || !string.Equals(id, id.Trim(), StringComparison.Ordinal))
                    throw new InvalidOperationException("地区请求包含空白或未规范化的节点 ID。");
                if (!seen.Add(id))
                    throw new InvalidOperationException("地区请求包含重复节点 ID：" + id);
                ids.Add(id);
            }
            if (ids.Count == 0)
                throw new InvalidOperationException("地区请求必须至少包含一个节点 ID。");
            if (ids.Count > 100000)
                throw new InvalidOperationException("地区请求节点数量超过夹具上限。");
            return ids.ToArray();
        }

        private static void ValidateRunnerConfigs(string value)
        {
            if (string.IsNullOrWhiteSpace(value))
                throw new InvalidOperationException("缺少 -c 配置路径。");
            string[] paths = value.Split(new[] { ',' }, StringSplitOptions.RemoveEmptyEntries);
            if (paths.Length == 0)
                throw new InvalidOperationException("-c 没有有效配置路径。");
            foreach (string rawPath in paths)
            {
                string path = RequireSandboxFile(rawPath.Trim(), "runner 配置");
                if (!File.Exists(path))
                    throw new FileNotFoundException("runner 配置不存在。", path);
            }
        }

        private static string ReadScenario(
            string fileName, string defaultValue, IEnumerable<string> allowedValues)
        {
            string path = Path.Combine(FixtureRoot(), "control", fileName);
            string value = File.Exists(path)
                ? File.ReadAllText(path, Utf8).Trim().ToLowerInvariant()
                : defaultValue;
            foreach (string allowed in allowedValues)
            {
                if (string.Equals(value, allowed, StringComparison.Ordinal)) return value;
            }
            throw new InvalidOperationException(
                "未知夹具场景 " + value + "（control/" + fileName + "）。");
        }

        private static void WaitForControlFile(string fileName)
        {
            string path = Path.Combine(FixtureRoot(), "control", fileName);
            while (!File.Exists(path)) Thread.Sleep(25);
        }

        private static void BlockForever()
        {
            while (true) Thread.Sleep(1000);
        }

        private static void WriteSignal(
            string fileName, string role, string mode, string detail)
        {
            string directory = Path.Combine(FixtureRoot(), "signals");
            Directory.CreateDirectory(directory);
            object value = Dictionary(
                "pid", Process.GetCurrentProcess().Id,
                "utc", DateTime.UtcNow.ToString("o", CultureInfo.InvariantCulture),
                "role", role,
                "mode", mode,
                "detail", detail ?? "");
            string destination = Path.Combine(directory, fileName);
            string temporary = destination + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try
            {
                File.WriteAllText(temporary, Json.Serialize(value), Utf8);
                if (File.Exists(destination))
                    File.Replace(temporary, destination, null);
                else
                    File.Move(temporary, destination);
            }
            finally
            {
                if (File.Exists(temporary)) File.Delete(temporary);
            }
        }

        private static void WriteEvent(StreamWriter output, string prefix, object value)
        {
            string json = Json.Serialize(value);
            string encoded = Convert.ToBase64String(Utf8.GetBytes(json)).TrimEnd('=');
            output.WriteLine(prefix + "\t" + encoded);
            output.Flush();
        }

        private static string FixtureRoot()
        {
            string raw = Environment.GetEnvironmentVariable(FixtureRootEnvironment);
            if (string.IsNullOrWhiteSpace(raw))
                throw new InvalidOperationException(
                    "缺少环境变量 " + FixtureRootEnvironment + "。");
            string root = Path.GetFullPath(raw).TrimEnd(
                Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
            if (!Directory.Exists(root))
                throw new DirectoryNotFoundException("UI 夹具沙箱不存在：" + root);
            return root;
        }

        private static string RequireSandboxFile(string path, string description)
        {
            return RequireSandboxPath(path, description);
        }

        private static string RequireSandboxPath(string path, string description)
        {
            if (string.IsNullOrWhiteSpace(path))
                throw new InvalidOperationException(description + "路径为空。");
            string fullPath = Path.GetFullPath(path);
            string root = FixtureRoot();
            string rootPrefix = root + Path.DirectorySeparatorChar;
            if (!string.Equals(fullPath, root, StringComparison.OrdinalIgnoreCase)
                && !fullPath.StartsWith(rootPrefix, StringComparison.OrdinalIgnoreCase))
                throw new InvalidOperationException(description + "必须位于 UI 夹具沙箱内：" + fullPath);
            return fullPath;
        }

        private static void EnsureParentDirectory(string path)
        {
            string directory = Path.GetDirectoryName(path);
            if (string.IsNullOrWhiteSpace(directory))
                throw new InvalidOperationException("无法确定输出目录：" + path);
            Directory.CreateDirectory(directory);
        }

        private static string BuildYaml(IEnumerable<NodeDefinition> nodes)
        {
            StringBuilder yaml = new StringBuilder();
            yaml.AppendLine("proxies:");
            foreach (NodeDefinition node in nodes)
            {
                if (string.Equals(node.Id, NodeA.Id, StringComparison.Ordinal))
                {
                    yaml.AppendLine("  - name: " + QuoteYaml(node.Name));
                    yaml.AppendLine("    type: ss");
                    yaml.AppendLine("    server: 127.0.0.1");
                    yaml.AppendLine("    port: 10001");
                    yaml.AppendLine("    cipher: aes-128-gcm");
                    yaml.AppendLine("    password: fixture-a");
                    yaml.AppendLine("    udp: true");
                }
                else if (string.Equals(node.Id, NodeB.Id, StringComparison.Ordinal))
                {
                    yaml.AppendLine("  - name: " + QuoteYaml(node.Name));
                    yaml.AppendLine("    type: trojan");
                    yaml.AppendLine("    server: 127.0.0.1");
                    yaml.AppendLine("    port: 10002");
                    yaml.AppendLine("    password: fixture-b");
                    yaml.AppendLine("    sni: fixture.invalid");
                    yaml.AppendLine("    skip-cert-verify: true");
                    yaml.AppendLine("    udp: true");
                }
                else if (string.Equals(node.Id, NodeC.Id, StringComparison.Ordinal))
                {
                    yaml.AppendLine("  - name: " + QuoteYaml(node.Name));
                    yaml.AppendLine("    type: vless");
                    yaml.AppendLine("    server: 127.0.0.1");
                    yaml.AppendLine("    port: 10003");
                    yaml.AppendLine("    uuid: 00000000-0000-4000-8000-000000000003");
                    yaml.AppendLine("    network: tcp");
                    yaml.AppendLine("    tls: false");
                    yaml.AppendLine("    udp: true");
                }
            }
            return yaml.ToString();
        }

        private static Dictionary<string, string> ReadRenames(object raw)
        {
            Dictionary<string, object> values = raw as Dictionary<string, object>;
            if (values == null)
                throw new InvalidOperationException("节点管理 renames 必须是对象。");
            Dictionary<string, string> result = new Dictionary<string, string>(StringComparer.Ordinal);
            foreach (KeyValuePair<string, object> pair in values)
            {
                string name = pair.Value as string;
                if (string.IsNullOrWhiteSpace(pair.Key)
                    || !string.Equals(pair.Key, pair.Key.Trim(), StringComparison.Ordinal)
                    || string.IsNullOrWhiteSpace(name)
                    || !string.Equals(name, name.Trim(), StringComparison.Ordinal)
                    || name.Any(char.IsControl))
                    throw new InvalidOperationException("节点管理 renames 包含无效 ID 或名称。");
                result.Add(pair.Key, name);
            }
            return result;
        }

        private static HashSet<string> ReadDeletes(object raw)
        {
            IEnumerable values = raw as IEnumerable;
            if (values == null || raw is string)
                throw new InvalidOperationException("节点管理 deletes 必须是数组。");
            HashSet<string> result = new HashSet<string>(StringComparer.Ordinal);
            foreach (object value in values)
            {
                string id = value as string;
                if (string.IsNullOrWhiteSpace(id)
                    || !string.Equals(id, id.Trim(), StringComparison.Ordinal)
                    || !result.Add(id))
                    throw new InvalidOperationException("节点管理 deletes 包含空白或重复 ID。");
            }
            return result;
        }

        private static NodeDefinition[] LoadManagedNodes(string path)
        {
            string yaml = File.ReadAllText(path, Utf8);
            List<NodeDefinition> nodes = new List<NodeDefinition>();
            AddManagedNodeIfPresent(nodes, yaml, NodeA, "10001");
            AddManagedNodeIfPresent(nodes, yaml, NodeB, "10002");
            return nodes.ToArray();
        }

        private static void AddManagedNodeIfPresent(
            ICollection<NodeDefinition> nodes, string yaml, NodeDefinition template, string port)
        {
            string marker = "    port: " + port;
            int portIndex = yaml.IndexOf(marker, StringComparison.Ordinal);
            if (portIndex < 0) return;
            int blockStart = yaml.LastIndexOf("  - name: ", portIndex, StringComparison.Ordinal);
            if (blockStart < 0)
                throw new InvalidOperationException("夹具管理配置缺少节点名称：" + port);
            int valueStart = blockStart + "  - name: ".Length;
            int lineEnd = yaml.IndexOf('\n', valueStart);
            if (lineEnd < 0) lineEnd = yaml.Length;
            string rawName = yaml.Substring(valueStart, lineEnd - valueStart).Trim();
            string name = UnquoteYaml(rawName);
            nodes.Add(template.WithName(name));
        }

        private static string QuoteYaml(string value)
        {
            return "\"" + (value ?? "").Replace("\\", "\\\\").Replace("\"", "\\\"") + "\"";
        }

        private static string UnquoteYaml(string value)
        {
            if (value.Length >= 2 && value[0] == '"' && value[value.Length - 1] == '"')
                return value.Substring(1, value.Length - 2)
                    .Replace("\\\"", "\"").Replace("\\\\", "\\");
            return value;
        }

        private static StreamWriter NewStandardWriter(Stream stream)
        {
            return new StreamWriter(stream, new UTF8Encoding(false)) { AutoFlush = true };
        }

        private static Dictionary<string, object> Dictionary(params object[] values)
        {
            if (values == null || values.Length % 2 != 0)
                throw new ArgumentException("字典键值必须成对出现。", "values");
            Dictionary<string, object> result =
                new Dictionary<string, object>(StringComparer.Ordinal);
            for (int index = 0; index < values.Length; index += 2)
            {
                string key = values[index] as string;
                if (string.IsNullOrEmpty(key))
                    throw new ArgumentException("字典键不能为空。", "values");
                result.Add(key, values[index + 1]);
            }
            return result;
        }

        private sealed class NodeDefinition
        {
            public string Id;
            public string Name;
            public string Type;
            public string ShareUrl;
            public Dictionary<string, object> Config;

            public object ToManifest()
            {
                return Dictionary(
                    "id", Id,
                    "name", Name,
                    "type", Type,
                    "share_url", ShareUrl,
                    "share_error", "",
                    "config", Config);
            }

            public NodeDefinition WithName(string name)
            {
                Dictionary<string, object> config = new Dictionary<string, object>(
                    Config, StringComparer.Ordinal);
                config["name"] = name;
                string shareUrl = ShareUrl;
                int fragment = shareUrl == null ? -1 : shareUrl.IndexOf('#');
                if (fragment >= 0)
                    shareUrl = shareUrl.Substring(0, fragment) + "#" + Uri.EscapeDataString(name);
                return new NodeDefinition
                {
                    Id = Id,
                    Name = name,
                    Type = Type,
                    ShareUrl = shareUrl,
                    Config = config
                };
            }
        }

        private sealed class FixtureArguments
        {
            private readonly Dictionary<string, string> values =
                new Dictionary<string, string>(StringComparer.Ordinal);

            public static FixtureArguments Parse(string[] args)
            {
                FixtureArguments result = new FixtureArguments();
                for (int index = 0; index < args.Length; index++)
                {
                    string token = args[index] ?? "";
                    if (!token.StartsWith("-", StringComparison.Ordinal))
                        throw new InvalidOperationException("无法识别的参数：" + token);

                    string key = token;
                    string value = "true";
                    int equals = token.IndexOf('=');
                    if (equals > 0)
                    {
                        key = token.Substring(0, equals);
                        value = token.Substring(equals + 1);
                    }
                    else if (index + 1 < args.Length
                        && !(args[index + 1] ?? "").StartsWith("-", StringComparison.Ordinal))
                    {
                        value = args[++index];
                    }

                    if (result.values.ContainsKey(key))
                        throw new InvalidOperationException("参数重复：" + key);
                    result.values.Add(key, value);
                }
                return result;
            }

            public bool Has(string name)
            {
                return values.ContainsKey(name);
            }

            public string Get(string name, string defaultValue)
            {
                string value;
                return values.TryGetValue(name, out value) ? value : defaultValue;
            }

            public string Require(string name)
            {
                string value;
                if (!values.TryGetValue(name, out value) || string.IsNullOrWhiteSpace(value)
                    || string.Equals(value, "true", StringComparison.Ordinal))
                    throw new InvalidOperationException("缺少参数 " + name + "。");
                return value;
            }
        }
    }
}

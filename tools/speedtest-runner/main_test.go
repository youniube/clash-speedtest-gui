package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"clash-speedtest.local/speedtest-runner/internal/upstream/output"
	"clash-speedtest.local/speedtest-runner/internal/upstream/speedtester"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"gopkg.in/yaml.v2"
)

func TestWriteManifestIncludesCompleteNodeEvent(t *testing.T) {
	config := map[string]any{
		"name":     "node-a",
		"type":     "ss",
		"server":   "ss.example.com",
		"port":     443,
		"cipher":   "aes-128-gcm",
		"password": "secret",
		"plugin":   "v2ray-plugin",
		"plugin-opts": map[string]any{
			"mode": "websocket",
			"host": "cdn.example.com",
			"path": "/socket",
			"tls":  true,
		},
	}
	proxy, err := adapter.ParseProxy(config)
	if err != nil {
		t.Fatal(err)
	}
	proxies := map[string]*speedtester.CProxy{
		"node-a": {Proxy: proxy, Config: config},
	}

	var buffer bytes.Buffer
	writer := bufio.NewWriter(&buffer)
	ids, err := writeManifest(writer, proxies)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if ids["node-a"] == "" {
		t.Fatal("stable node ID is missing")
	}

	var event nodeEvent
	for _, line := range strings.Split(buffer.String(), "\n") {
		if !strings.HasPrefix(line, "@nodejson\t") {
			continue
		}
		body, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(line, "@nodejson\t"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
	}
	if event.ID != ids["node-a"] || event.Name != "node-a" {
		t.Fatalf("unexpected node event: %#v", event)
	}
	pluginOpts, ok := event.Config["plugin-opts"].(map[string]any)
	if !ok || pluginOpts["path"] != "/socket" || pluginOpts["host"] != "cdn.example.com" {
		t.Fatalf("nested node configuration was lost: %#v", event.Config)
	}
	if !strings.HasPrefix(event.ShareURL, "ss://") || !strings.Contains(event.ShareURL, "cdn.example.com") {
		t.Fatalf("share URL was not generated from node config: %s (%s)", event.ShareURL, event.ShareError)
	}
}

type alwaysFailWriter struct{}

func (alwaysFailWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("forced writer failure")
}

func TestWriteEventReportsSerializationAndWriterErrors(t *testing.T) {
	var buffer bytes.Buffer
	writer := bufio.NewWriter(&buffer)
	if err := writeEvent(writer, "@broken", map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("JSON serialization error must be returned")
	}

	failing := bufio.NewWriterSize(alwaysFailWriter{}, 1)
	if err := writeEvent(failing, "@event", map[string]any{"ok": true}); err == nil {
		t.Fatal("writer error must be returned")
	}
}

func TestWriteManifestReportsWriterError(t *testing.T) {
	failing := bufio.NewWriterSize(alwaysFailWriter{}, 1)
	if _, err := writeManifest(failing, map[string]*speedtester.CProxy{}); err == nil {
		t.Fatal("manifest writer error must be returned")
	}
}

func TestSanitizeLegacyTSVCellsKeepsOnePhysicalRow(t *testing.T) {
	row := []string{"1.", "node\tname\r\nsecond line", "ss", "20ms"}
	sanitized := sanitizeLegacyTSVCells(row)
	encoded := strings.Join(sanitized, "\t") + "\n"
	if strings.Count(encoded, "\n") != 1 {
		t.Fatalf("legacy TSV row must contain exactly one line ending: %q", encoded)
	}
	if got := strings.Split(strings.TrimSuffix(encoded, "\n"), "\t"); len(got) != len(row) {
		t.Fatalf("legacy TSV column count changed: got %d, want %d", len(got), len(row))
	}
}

func TestBuildResultEventV4PreservesRawMetrics(t *testing.T) {
	result := &speedtester.Result{
		Latency:          1500 * time.Microsecond,
		Jitter:           250 * time.Microsecond,
		PacketLoss:       12.34,
		DownloadSpeed:    3.5 * 1024 * 1024,
		UploadSpeed:      1.25 * 1024 * 1024,
		DownloadTested:   true,
		UploadTested:     true,
		DownloadComplete: true,
		UploadComplete:   true,
	}
	row := []string{"1.", "node-a", "ss", "1ms", "0ms", "12.3%", "3.50MB/s", "1.25MB/s"}
	event := buildResultEvent("stable-id", row, result, speedtester.SpeedModeFull, true)
	if speedEventProtocolVersion != 4 {
		t.Fatalf("speed event protocol version=%d, want 4", speedEventProtocolVersion)
	}
	if event.ID != "stable-id" || !event.Usable || event.Metrics.LatencyNanoseconds != int64(result.Latency) ||
		event.Metrics.JitterNanoseconds != int64(result.Jitter) || event.Metrics.PacketLossPercent != 12.34 ||
		event.Metrics.DownloadBytesPerSecond != result.DownloadSpeed ||
		event.Metrics.UploadBytesPerSecond != result.UploadSpeed ||
		!event.Metrics.DownloadTested || !event.Metrics.UploadTested ||
		!event.Metrics.DownloadComplete || !event.Metrics.UploadComplete {
		t.Fatalf("unexpected v4 result event: %#v", event)
	}

	download := buildResultEvent("download", row[:7], result, speedtester.SpeedModeDownload, false)
	if !download.Metrics.DownloadTested || download.Metrics.UploadTested ||
		download.Metrics.UploadBytesPerSecond != 0 || download.Usable {
		t.Fatalf("download mode flags are wrong: %#v", download.Metrics)
	}
	fast := buildResultEvent("fast", row[:4], result, speedtester.SpeedModeFast, false)
	if fast.Metrics.DownloadTested || fast.Metrics.UploadTested ||
		fast.Metrics.DownloadBytesPerSecond != 0 || fast.Metrics.UploadBytesPerSecond != 0 {
		t.Fatalf("fast mode must not report transfer tests: %#v", fast.Metrics)
	}
	skipped := buildResultEvent("skipped", row, &speedtester.Result{Latency: time.Second},
		speedtester.SpeedModeFull, false)
	if skipped.Metrics.DownloadTested || skipped.Metrics.UploadTested {
		t.Fatalf("early exit must report skipped transfers: %#v", skipped.Metrics)
	}
	failed := buildResultEvent("failed", row[:7], &speedtester.Result{
		DownloadError: "timeout", DownloadTested: true, DownloadComplete: false,
	},
		speedtester.SpeedModeDownload, false)
	if !failed.Metrics.DownloadTested || failed.Metrics.DownloadComplete {
		t.Fatalf("attempted failed transfer must remain tested: %#v", failed.Metrics)
	}

	body, err := json.Marshal(fast)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"usable"`, `"metrics"`, `"latency_nanoseconds"`, `"packet_loss_percent"`,
		`"download_bytes_per_second"`, `"download_tested"`, `"upload_tested"`,
		`"download_complete"`, `"upload_complete"`,
	} {
		if !bytes.Contains(body, []byte(field)) {
			t.Fatalf("v4 JSON is missing %s: %s", field, body)
		}
	}
}

func TestResultEventV4UsesRawThresholdDecision(t *testing.T) {
	previousLatency := *maxLatency
	previousDownloadSize := *downloadSize
	previousMinDownload := *minDownloadSpeed
	defer func() {
		*maxLatency = previousLatency
		*downloadSize = previousDownloadSize
		*minDownloadSpeed = previousMinDownload
	}()

	*maxLatency = time.Second
	latencyBoundary := &speedtester.Result{
		Latency:    time.Second + 900*time.Microsecond,
		PacketLoss: 0,
	}
	latencyRow := output.FormatRow(latencyBoundary, speedtester.SpeedModeFast, 0)
	latencyEvent := buildResultEvent("latency", latencyRow, latencyBoundary,
		speedtester.SpeedModeFast, isUsable(latencyBoundary, speedtester.SpeedModeFast))
	if latencyRow[3] != "1000ms" || latencyEvent.Usable ||
		latencyEvent.Metrics.LatencyNanoseconds != int64(latencyBoundary.Latency) {
		t.Fatalf("latency boundary lost raw decision: row=%q event=%#v", latencyRow, latencyEvent)
	}

	*downloadSize = 20 * 1024 * 1024
	*minDownloadSpeed = 5
	downloadBoundary := &speedtester.Result{
		Latency:          20 * time.Millisecond,
		PacketLoss:       0,
		DownloadSpeed:    5*1024*1024 - 1,
		DownloadTime:     time.Second,
		DownloadTested:   true,
		DownloadComplete: true,
	}
	downloadRow := output.FormatRow(downloadBoundary, speedtester.SpeedModeDownload, 0)
	downloadEvent := buildResultEvent("download", downloadRow, downloadBoundary,
		speedtester.SpeedModeDownload, isUsable(downloadBoundary, speedtester.SpeedModeDownload))
	if downloadRow[6] != "5.00MB/s" || downloadEvent.Usable ||
		downloadEvent.Metrics.DownloadBytesPerSecond != downloadBoundary.DownloadSpeed {
		t.Fatalf("download boundary lost raw decision: row=%q event=%#v", downloadRow, downloadEvent)
	}
}

func TestFastModeRejectsUnreachableNode(t *testing.T) {
	mode, err := speedtester.ParseSpeedMode("fast")
	if err != nil {
		t.Fatal(err)
	}
	result := &speedtester.Result{Latency: 0, PacketLoss: 100}
	if isUsable(result, mode) {
		t.Fatal("unreachable node must not be exported")
	}
}

func TestFastModeKeepsReachableNode(t *testing.T) {
	mode, err := speedtester.ParseSpeedMode("fast")
	if err != nil {
		t.Fatal(err)
	}
	result := &speedtester.Result{Latency: 100 * time.Millisecond, PacketLoss: 0}
	if !isUsable(result, mode) {
		t.Fatal("reachable node should be exported")
	}
}

func TestTwoStageRunnerSerializesNodeTransfers(t *testing.T) {
	var activeTransfers atomic.Int32
	var maxActiveTransfers atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__down" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("bytes") == "1" {
			_, _ = w.Write([]byte{0})
			return
		}
		active := activeTransfers.Add(1)
		for {
			previous := maxActiveTransfers.Load()
			if active <= previous || maxActiveTransfers.CompareAndSwap(previous, active) {
				break
			}
		}
		defer activeTransfers.Add(-1)
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write(make([]byte, 4*1024))
	}))
	defer server.Close()

	tester, err := speedtester.New(&speedtester.Config{
		ServerURL: server.URL, Mode: speedtester.SpeedModeDownload,
		DownloadSize: 8 * 1024, Timeout: 2 * time.Second, Concurrent: 2,
		OutputPath: "result.yaml", MaxPacketLoss: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies := map[string]*speedtester.CProxy{
		"node-a": {
			Proxy:  adapter.NewProxy(outbound.NewDirect()),
			Config: map[string]any{"name": "node-a", "type": "direct", "server": "a"},
		},
		"node-b": {
			Proxy:  adapter.NewProxy(outbound.NewDirect()),
			Config: map[string]any{"name": "node-b", "type": "direct", "server": "b"},
		},
	}
	previousDownloadSize := *downloadSize
	previousMinDownload := *minDownloadSpeed
	defer func() {
		*downloadSize = previousDownloadSize
		*minDownloadSpeed = previousMinDownload
	}()
	*downloadSize = 8 * 1024
	*minDownloadSpeed = 0

	var output bytes.Buffer
	results, err := testInStages(tester, proxies,
		map[string]string{"node-a": strings.Repeat("a", 64), "node-b": strings.Repeat("b", 64)},
		speedtester.SpeedModeDownload, bufio.NewWriter(&output), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("result count=%d, want 2", len(results))
	}
	if maxActiveTransfers.Load() > 2 {
		t.Fatalf("node transfers overlapped: max active requests=%d, per-node limit=2", maxActiveTransfers.Load())
	}
	for _, result := range results {
		if !result.DownloadComplete || result.DownloadSpeed <= 0 {
			t.Fatalf("incomplete staged transfer: %#v", result)
		}
	}
}

func TestValidateSpeedOptionsRejectsUnsafeValues(t *testing.T) {
	previousTimeout := *timeout
	previousNodeParallel := *nodeParallel
	previousTransferParallel := *transferParallel
	previousDownloadSize := *downloadSize
	previousUploadSize := *uploadSize
	previousPacketLoss := *maxPacketLoss
	defer func() {
		*timeout = previousTimeout
		*nodeParallel = previousNodeParallel
		*transferParallel = previousTransferParallel
		*downloadSize = previousDownloadSize
		*uploadSize = previousUploadSize
		*maxPacketLoss = previousPacketLoss
	}()

	*timeout = time.Second
	*nodeParallel = 4
	*transferParallel = 4
	*downloadSize = 1024
	*uploadSize = 1024
	*maxPacketLoss = 100
	if err := validateSpeedOptions(speedtester.SpeedModeFull); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	*transferParallel = speedtester.MaxTransferConcurrency + 1
	if err := validateSpeedOptions(speedtester.SpeedModeFull); err == nil {
		t.Fatal("unsafe transfer concurrency must be rejected")
	}
	*transferParallel = 4
	*downloadSize = 0
	if err := validateSpeedOptions(speedtester.SpeedModeDownload); err == nil {
		t.Fatal("zero download size must be rejected in download mode")
	}
	*downloadSize = 1024
	*maxPacketLoss = 101
	if err := validateSpeedOptions(speedtester.SpeedModeFull); err == nil {
		t.Fatal("failure rate over 100 must be rejected")
	}
}

func TestValidateFilterRegex(t *testing.T) {
	if err := validateFilterRegex("HK|香港"); err != nil {
		t.Fatalf("valid regex rejected: %v", err)
	}
	if err := validateFilterRegex("["); err == nil {
		t.Fatal("invalid regex must be rejected")
	}
}

func TestLoadProxiesKeepsSharedServerPortNodes(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "shared-endpoint.yaml")
	config := []byte(`proxies:
  - name: node-a
    type: ss
    server: shared.example.com
    port: 443
    cipher: aes-128-gcm
    password: password-a
  - name: node-b
    type: ss
    server: shared.example.com
    port: 443
    cipher: aes-128-gcm
    password: password-b
`)
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}

	tester, err := speedtester.New(&speedtester.Config{
		ConfigPaths: configPath,
		FilterRegex: ".+",
		ServerURL:   "https://example.com/test.bin",
		Mode:        speedtester.SpeedModeFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 2 {
		t.Fatalf("shared server:port nodes with different credentials must both remain, got %d", len(proxies))
	}
}

func TestLoadProxiesKeepsSameNameNodesAcrossSources(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.yaml")
	secondPath := filepath.Join(directory, "second.yaml")
	first := []byte(`proxies:
  - name: shared-name
    type: ss
    server: first.example.com
    port: 443
    cipher: aes-128-gcm
    password: password-a
`)
	second := []byte(`proxies:
  - name: shared-name
    type: ss
    server: second.example.com
    port: 443
    cipher: aes-128-gcm
    password: password-b
  - name: shared-name [2]
    type: ss
    server: reserved.example.com
    port: 443
    cipher: aes-128-gcm
    password: password-c
`)
	if err := os.WriteFile(firstPath, first, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, second, 0600); err != nil {
		t.Fatal(err)
	}

	tester, err := speedtester.New(&speedtester.Config{
		ConfigPaths: firstPath + "," + secondPath,
		FilterRegex: ".+",
		ServerURL:   "https://example.com/file.bin",
		Mode:        speedtester.SpeedModeFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shared-name", "shared-name [2]", "shared-name [3]"} {
		proxy, ok := proxies[name]
		if !ok {
			t.Fatalf("missing merged node %q: %#v", name, proxies)
		}
		if proxy.Config["name"] != name {
			t.Fatalf("merged config name=%v, want %q", proxy.Config["name"], name)
		}
	}
	if len(proxies) != 3 {
		t.Fatalf("same-name nodes must be preserved, got %d", len(proxies))
	}
}

func TestDeduplicateProxiesByCompleteConfig(t *testing.T) {
	base := func(name string) map[string]any {
		return map[string]any{
			"name":       name,
			"type":       "vless",
			"server":     "shared.example.com",
			"port":       443,
			"uuid":       "00000000-0000-0000-0000-000000000000",
			"network":    "ws",
			"servername": "edge.example.com",
			"ws-opts": map[string]any{
				"path": "/node-a",
				"headers": map[string]any{
					"Host": "edge.example.com",
				},
			},
		}
	}

	t.Run("different names with otherwise identical config are duplicates", func(t *testing.T) {
		first := base("node-a")
		second := base("node-b")
		proxies := map[string]*speedtester.CProxy{
			"node-a": {Config: first},
			"node-b": {Config: second},
		}

		result := deduplicateProxiesByConfig(proxies)
		if len(result) != 1 {
			t.Fatalf("expected one proxy, got %d", len(result))
		}
		if _, exists := result["node-a"]; !exists {
			t.Fatal("deduplication must deterministically keep the lexicographically first name")
		}
	})

	tests := []struct {
		name   string
		change func(map[string]any)
	}{
		{
			name: "different credentials",
			change: func(config map[string]any) {
				config["uuid"] = "11111111-1111-1111-1111-111111111111"
			},
		},
		{
			name: "different SNI",
			change: func(config map[string]any) {
				config["servername"] = "other.example.com"
			},
		},
		{
			name: "different transport path",
			change: func(config map[string]any) {
				config["ws-opts"].(map[string]any)["path"] = "/node-b"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := base("node-a")
			second := base("node-b")
			test.change(second)
			proxies := map[string]*speedtester.CProxy{
				"node-a": {Config: first},
				"node-b": {Config: second},
			}
			if result := deduplicateProxiesByConfig(proxies); len(result) != 2 {
				t.Fatalf("different complete configurations must remain, got %d", len(result))
			}
		})
	}
}

func TestApplyNodeManagementRenamesAndDeletesByStableID(t *testing.T) {
	first := map[string]any{
		"name": "node-a", "type": "ss", "server": "a.example.com", "port": 443,
		"cipher": "aes-128-gcm", "password": "password-a",
	}
	second := map[string]any{
		"name": "node-b", "type": "ss", "server": "b.example.com", "port": 8443,
		"cipher": "aes-128-gcm", "password": "password-b",
	}
	firstFingerprint, err := configFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := configFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	firstID := fmt.Sprintf("%x", firstFingerprint)
	secondID := fmt.Sprintf("%x", secondFingerprint)

	updated, renamed, deleted, err := applyNodeManagement(
		[]map[string]any{first, second},
		&nodeManagementRequest{
			Renames: map[string]string{firstID: "renamed-node"},
			Deletes: []string{secondID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if renamed != 1 || deleted != 1 || len(updated) != 1 {
		t.Fatalf("unexpected management counts: renamed=%d deleted=%d nodes=%d", renamed, deleted, len(updated))
	}
	if updated[0]["name"] != "renamed-node" {
		t.Fatalf("rename was not applied: %#v", updated[0])
	}
	afterFingerprint, err := configFingerprint(updated[0])
	if err != nil {
		t.Fatal(err)
	}
	if afterFingerprint != firstFingerprint {
		t.Fatal("renaming must not change the stable configuration ID")
	}
}

func TestApplyNodeManagementRejectsUnknownAndDuplicateNames(t *testing.T) {
	proxies := []map[string]any{
		{"name": "node-a", "type": "ss", "server": "a.example.com", "port": 443},
		{"name": "node-b", "type": "ss", "server": "b.example.com", "port": 443},
	}
	fingerprint, err := configFingerprint(proxies[0])
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%x", fingerprint)

	if _, _, _, err := applyNodeManagement(proxies, &nodeManagementRequest{
		Renames: map[string]string{"missing": "new-name"},
	}); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("unknown ID must be rejected, got %v", err)
	}
	if _, _, _, err := applyNodeManagement(proxies, &nodeManagementRequest{
		Renames: map[string]string{id: "node-b"},
	}); err == nil || !strings.Contains(err.Error(), "duplicate node name") {
		t.Fatalf("duplicate name must be rejected, got %v", err)
	}
}

func TestManageLocalConfigPreservesNestedFieldsAndReturnsShareURL(t *testing.T) {
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "input.yaml")
	outputPath := filepath.Join(directory, "output.yaml")
	requestPath := filepath.Join(directory, "request.json")
	config := map[string]any{
		"name": "node-a", "type": "ss", "server": "ss.example.com", "port": 443,
		"cipher": "aes-128-gcm", "password": "password-a",
		"plugin":      "v2ray-plugin",
		"plugin-opts": map[string]any{"mode": "websocket", "host": "cdn.example.com", "path": "/socket"},
	}
	fingerprint, err := configFingerprint(config)
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%x", fingerprint)
	yamlBody, err := yaml.Marshal(&managedConfig{Proxies: []map[string]any{config}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, yamlBody, 0600); err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(&nodeManagementRequest{
		Renames: map[string]string{id: "renamed-node"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, requestBody, 0600); err != nil {
		t.Fatal(err)
	}

	result, err := manageLocalConfig(inputPath, outputPath, requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Renamed != 1 || len(result.Nodes) != 1 || result.Nodes[0].Name != "renamed-node" {
		t.Fatalf("unexpected management result: %#v", result)
	}
	if !strings.HasPrefix(result.Nodes[0].ShareURL, "ss://") ||
		!strings.Contains(result.Nodes[0].ShareURL, "cdn.example.com") {
		t.Fatalf("updated share URL is invalid: %s (%s)", result.Nodes[0].ShareURL, result.Nodes[0].ShareError)
	}
	pluginOpts, ok := result.Nodes[0].Config["plugin-opts"].(map[string]any)
	if !ok || pluginOpts["path"] != "/socket" {
		t.Fatalf("nested fields were not preserved: %#v", result.Nodes[0].Config)
	}
}

func TestSortResultsDoesNotDeduplicateByServerPort(t *testing.T) {
	results := []*speedtester.Result{
		{
			ProxyName:     "node-a",
			Latency:       100 * time.Millisecond,
			DownloadSpeed: 20,
			ProxyConfig: map[string]any{
				"server": "shared.example.com",
				"port":   443,
				"uuid":   "00000000-0000-0000-0000-000000000000",
			},
		},
		{
			ProxyName:     "node-b",
			Latency:       110 * time.Millisecond,
			DownloadSpeed: 10,
			ProxyConfig: map[string]any{
				"server": "shared.example.com",
				"port":   443,
				"uuid":   "11111111-1111-1111-1111-111111111111",
			},
		},
	}

	if sorted := output.SortResults(results, speedtester.SpeedModeDownload); len(sorted) != 2 {
		t.Fatalf("sorting must not deduplicate distinct proxy configs, got %d", len(sorted))
	}
}

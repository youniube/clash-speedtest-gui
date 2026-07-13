package speedtester

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
)

func TestTransferSummaryAdd(t *testing.T) {
	summary := newTransferSummary()
	summary.add(nil)

	errorMessage := "download request to https://example.com/__down?bytes=1 failed: boom"
	summary.add(&downloadResult{error: errorMessage})
	if summary.successCount != 0 {
		t.Fatalf("expected successCount to remain 0, got %d", summary.successCount)
	}
	if len(summary.errors) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(summary.errors))
	}
	if summary.errors[0] != errorMessage {
		t.Fatalf("expected error message %q, got %q", errorMessage, summary.errors[0])
	}

	summary.add(&downloadResult{error: errorMessage})
	if len(summary.errors) != 1 {
		t.Fatalf("expected duplicate errors to be deduplicated, got %d", len(summary.errors))
	}

	summary.add(&downloadResult{bytes: 100, duration: time.Second})
	summary.add(&downloadResult{bytes: 50, duration: 2 * time.Second})

	if summary.successCount != 2 {
		t.Fatalf("expected successCount to be 2, got %d", summary.successCount)
	}
	if summary.totalBytes != 150 {
		t.Fatalf("expected totalBytes to be 150, got %d", summary.totalBytes)
	}
	size, duration, speed, transferError, complete := applyTransferSummary(summary, 2*time.Second, 3)
	if transferError == "" {
		t.Fatal("an explicit connection error must still fail the transfer")
	}
	if size != 150 || duration != 2*time.Second || speed != 75 || complete {
		t.Fatalf("unexpected failed transfer summary: size=%v duration=%v speed=%v", size, duration, speed)
	}
}

func TestTransferSummaryUsesBatchWallClockDuration(t *testing.T) {
	summary := newTransferSummary()
	summary.add(&downloadResult{bytes: 100, duration: time.Second})
	summary.add(&downloadResult{bytes: 100, duration: 4 * time.Second})

	size, duration, speed, transferError, complete := applyTransferSummary(summary, 4*time.Second, 2)
	if transferError != "" {
		t.Fatalf("unexpected transfer error: %s", transferError)
	}
	if size != 200 || duration != 4*time.Second {
		t.Fatalf("unexpected transfer totals: size=%v duration=%v", size, duration)
	}
	if speed != 50 || !complete {
		t.Fatalf("speed must use batch wall-clock duration: got %v, want 50", speed)
	}
}

func TestLatencyUsesRequestTimeoutInsteadOfFilterThreshold(t *testing.T) {
	tester, err := New(&Config{
		ServerURL:       "https://example.com/file.bin",
		Mode:            SpeedModeFast,
		ProbeTimeout:    3 * time.Second,
		DownloadTimeout: 11 * time.Second,
		MaxLatency:      0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tester.latencyRequestTimeout() != 3*time.Second {
		t.Fatalf("latency timeout=%v, want 3s", tester.latencyRequestTimeout())
	}
	if tester.config.MaxLatency != 0 {
		t.Fatal("latency filter threshold must remain independent from request timeout")
	}
	if tester.config.DownloadTimeout != 11*time.Second {
		t.Fatalf("download timeout=%v, want 11s", tester.config.DownloadTimeout)
	}
}

func TestConsumeDownloadResponseEnforcesRangeAndSize(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		contentRange string
		body         string
		wantBytes    int64
		wantError    string
	}{
		{name: "valid partial response", status: http.StatusPartialContent,
			contentRange: "bytes 0-3/10", body: "abcd", wantBytes: 4},
		{name: "full response is capped", status: http.StatusOK,
			body: "abcdefghij", wantBytes: 4},
		{name: "short full response", status: http.StatusOK,
			body: "ab", wantBytes: 2, wantError: "shorter than requested"},
		{name: "missing content range", status: http.StatusPartialContent,
			body: "abcd", wantError: "Content-Range"},
		{name: "wrong content range", status: http.StatusPartialContent,
			contentRange: "bytes 1-4/10", body: "abcd", wantError: "Content-Range"},
		{name: "oversized content range", status: http.StatusPartialContent,
			contentRange: "bytes 0-4/10", body: "abcde", wantError: "Content-Range"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: test.status,
				Status:     http.StatusText(test.status),
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(test.body)),
			}
			if test.contentRange != "" {
				response.Header.Set("Content-Range", test.contentRange)
			}
			got, err := consumeDownloadResponse(response, 4)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected error containing %q, got %v", test.wantError, err)
				}
				if got != test.wantBytes {
					t.Fatalf("read bytes=%d, want %d on error", got, test.wantBytes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.wantBytes {
				t.Fatalf("read bytes=%d, want %d", got, test.wantBytes)
			}
		})
	}
}

func TestResultFormatErrors(t *testing.T) {
	result := &Result{}
	if result.FormatDownloadError() != "N/A" {
		t.Fatalf("expected empty download error to format as N/A, got %q", result.FormatDownloadError())
	}
	result.DownloadError = "download failed: timeout"
	if result.FormatDownloadError() != result.DownloadError {
		t.Fatalf("expected download error to pass through, got %q", result.FormatDownloadError())
	}

	result.DownloadSpeed = 1024
	if result.FormatDownloadSpeed() == result.DownloadError {
		t.Fatalf("partial download speed must remain visible, got %q", result.FormatDownloadSpeed())
	}
	if result.FormatDownloadSpeedValue() == result.DownloadError {
		t.Fatalf("expected download speed value to ignore error string")
	}
}

func TestFetchHTTPConfigRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "subscription unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	tester, err := New(&Config{
		ServerURL:    "https://example.com/file.bin",
		ProbeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = tester.fetchHTTPConfig(server.URL)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("expected status in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "subscription unavailable") {
		t.Fatalf("expected response detail in error, got %v", err)
	}
}

func TestHTTPURLDetection(t *testing.T) {
	if !isHTTPURL(" HTTPS://example.com/sub ") {
		t.Fatal("expected https URL to be detected")
	}
	if isHTTPURL("http-file.yaml") {
		t.Fatal("local file names starting with http must not be treated as URLs")
	}
}

func TestStringMapValueRequiresString(t *testing.T) {
	if value, ok := stringMapValue(map[string]any{"server": "example.com"}, "server"); !ok || value != "example.com" {
		t.Fatalf("expected string value, got %q %v", value, ok)
	}
	if _, ok := stringMapValue(map[string]any{"server": 1234}, "server"); ok {
		t.Fatal("non-string value must not be accepted")
	}
	if _, ok := stringMapValue(nil, "server"); ok {
		t.Fatal("nil map must not return a value")
	}
}

func TestPrepareProviderProxyConfigsExcludeTypeSkipsMissingType(t *testing.T) {
	configs, err := prepareProviderProxyConfigs([]map[string]any{
		{"name": "missing-type", "server": "missing.example.com"},
		{"name": "keep", "type": "trojan", "server": "keep.example.com"},
	}, map[string]any{"exclude-type": "ss"})
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("prepared configs=%d, want 1", len(configs))
	}
	if name, _ := stringMapValue(configs[0], "name"); name != "keep" {
		t.Fatalf("prepared proxy=%q, want keep", name)
	}
}

func TestLoadProxiesFetchesHTTPProviderOnceAndMapsFilteredOverride(t *testing.T) {
	var requests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `proxies:
  - name: drop-node
    type: trojan
    server: drop.example.com
    port: 443
    password: drop-password
    sni: drop.example.com
  - name: keep-node
    type: trojan
    server: keep.example.com
    port: 443
    password: keep-password
    sni: keep.example.com
`)
	}))
	defer providerServer.Close()

	configBody := fmt.Sprintf(`proxy-providers:
  remote:
    type: http
    url: %q
    filter: "^keep-node$"
    override:
      additional-prefix: "provider-"
      skip-cert-verify: true
proxies: []
`, providerServer.URL)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	tester, err := New(&Config{
		ConfigPaths:  configPath,
		FilterRegex:  ".*",
		ServerURL:    "https://example.com/file.bin",
		Mode:         SpeedModeFast,
		ProbeTimeout: 2 * time.Second,
		Concurrent:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider URL requests=%d, want exactly 1", got)
	}
	if len(proxies) != 1 {
		t.Fatalf("loaded proxies=%d, want 1: %#v", len(proxies), proxies)
	}
	loaded := proxies["[remote] provider-keep-node"]
	if loaded == nil {
		t.Fatalf("filtered and renamed provider proxy is missing: %#v", proxies)
	}
	if got, _ := stringMapValue(loaded.Config, "server"); got != "keep.example.com" {
		t.Fatalf("provider config mapped server=%q, want keep.example.com", got)
	}
	if got, _ := stringMapValue(loaded.Config, "name"); got != "[remote] provider-keep-node" {
		t.Fatalf("final provider config name=%q, want prefixed loaded name", got)
	}
	if skipVerify, ok := loaded.Config["skip-cert-verify"].(bool); !ok || !skipVerify {
		t.Fatalf("provider connection override was not preserved: %#v", loaded.Config["skip-cert-verify"])
	}
	if addr := loaded.Addr(); !strings.Contains(addr, "keep.example.com") {
		t.Fatalf("loaded proxy points at wrong server: %q", addr)
	}
}

func TestBaseServerDownloadModeEndToEndNeverUploads(t *testing.T) {
	var probeRequests atomic.Int32
	var downloadRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path == "/__up" {
			t.Errorf("pure download mode sent forbidden request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "uploads are forbidden", http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/__down":
			size, err := strconv.Atoi(r.URL.Query().Get("bytes"))
			if err != nil || size <= 0 {
				http.Error(w, "invalid size", http.StatusBadRequest)
				return
			}
			if r.Header.Get("Accept-Encoding") != "identity" {
				http.Error(w, "compressed transfer is not allowed", http.StatusBadRequest)
				return
			}
			if size == 1 {
				probeRequests.Add(1)
			} else {
				downloadRequests.Add(1)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(make([]byte, size))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tester, err := New(&Config{
		ServerURL:           server.URL,
		Mode:                SpeedModeDownload,
		DownloadSize:        32 * 1024,
		ProbeTimeout:        2 * time.Second,
		DownloadTimeout:     2 * time.Second,
		Concurrent:          2,
		OutputPath:          "result.yaml",
		MaxHTTPProbeFailure: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy := &CProxy{
		Proxy:  adapter.NewProxy(outbound.NewDirect()),
		Config: map[string]any{"name": "direct-test", "type": "direct", "server": "127.0.0.1"},
	}
	result := tester.ProbeProxy("direct-test", proxy)
	if result.Latency <= 0 || result.HTTPProbeFailurePercent != 0 {
		t.Fatalf("base-server probe failed: latency=%v failure-rate=%v", result.Latency, result.HTTPProbeFailurePercent)
	}
	if !tester.ShouldTestTransfers(result) {
		t.Fatal("reachable download-mode node must enter transfer phase")
	}
	tester.TestTransfers(result, proxy)
	if !result.DownloadTested || !result.DownloadComplete || result.DownloadSpeed <= 0 || result.DownloadError != "" {
		t.Fatalf("download did not complete: %#v", result)
	}
	if probeRequests.Load() != latencyWarmupRequests+latencyMeasuredRequests {
		t.Fatalf("probe requests=%d, want %d", probeRequests.Load(), latencyWarmupRequests+latencyMeasuredRequests)
	}
	if downloadRequests.Load() != 2 {
		t.Fatalf("download requests=%d, want 2", downloadRequests.Load())
	}
}

func TestLatencyProbeRejectsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	tester, err := New(&Config{
		ServerURL: server.URL, Mode: SpeedModeFast, ProbeTimeout: time.Second, Concurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy := &CProxy{Proxy: adapter.NewProxy(outbound.NewDirect()), Config: map[string]any{}}
	result := tester.ProbeProxy("failure", proxy)
	if result.Latency != 0 || result.HTTPProbeFailurePercent != 100 {
		t.Fatalf("HTTP 503 must be a failed probe: latency=%v rate=%v", result.Latency, result.HTTPProbeFailurePercent)
	}
}

func TestDirectLatencyProbeUsesOneByteGET(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Range") != "bytes=0-0" {
			t.Errorf("probe request=%s Range=%q, want one-byte GET", r.Method, r.Header.Get("Range"))
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer server.Close()
	tester, err := New(&Config{
		ServerURL: server.URL + "/download.bin", Mode: SpeedModeFast,
		ProbeTimeout: time.Second, Concurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy := &CProxy{Proxy: adapter.NewProxy(outbound.NewDirect()), Config: map[string]any{}}
	result := tester.ProbeProxy("direct", proxy)
	if result.Latency <= 0 || result.HTTPProbeFailurePercent != 0 {
		t.Fatalf("one-byte direct probe failed: %#v", result)
	}
	if requests.Load() != latencyWarmupRequests+latencyMeasuredRequests {
		t.Fatalf("request count=%d, want warmup plus five samples", requests.Load())
	}
}

func TestLatencyStatsUseMedianAndMeasuredDenominator(t *testing.T) {
	result := calculateLatencyStats([]time.Duration{
		10 * time.Millisecond, 12 * time.Millisecond, 200 * time.Millisecond, 11 * time.Millisecond,
	}, 1, 5)
	if result.latency != 11500*time.Microsecond {
		t.Fatalf("median latency=%v, want 11.5ms", result.latency)
	}
	if result.httpProbeFailurePercent != 20 {
		t.Fatalf("failure rate=%v, want 20", result.httpProbeFailurePercent)
	}
	if result.jitter < 81*time.Millisecond || result.jitter > 82*time.Millisecond {
		t.Fatalf("population standard deviation jitter=%v, want about 81.84ms", result.jitter)
	}
}

func TestSplitTransferSizesPreservesTotalAndLimit(t *testing.T) {
	chunks := splitTransferSizes(10, 4)
	if len(chunks) != 4 {
		t.Fatalf("chunk count=%d, want 4", len(chunks))
	}
	total := 0
	for _, chunk := range chunks {
		if chunk <= 0 {
			t.Fatalf("chunk must be positive: %v", chunks)
		}
		total += chunk
	}
	if total != 10 {
		t.Fatalf("chunk total=%d, want 10", total)
	}
	if tiny := splitTransferSizes(2, 4); len(tiny) != 2 || tiny[0] != 1 || tiny[1] != 1 {
		t.Fatalf("tiny transfer split=%v", tiny)
	}
}

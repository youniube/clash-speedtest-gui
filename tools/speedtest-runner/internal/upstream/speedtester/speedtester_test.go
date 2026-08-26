package speedtester

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mihomoTransportHTTP "github.com/metacubex/http"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	mihomoCA "github.com/metacubex/mihomo/component/ca"
	C "github.com/metacubex/mihomo/constant"
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

func TestPrepareProviderProxyConfigsTracksMatchesByRecordIndex(t *testing.T) {
	configs, err := prepareProviderProxyConfigs([]map[string]any{
		{"name": "shared", "type": "trojan", "server": "first.example.com"},
		{"name": "shared", "type": "trojan", "server": "second.example.com"},
	}, map[string]any{"filter": "^shared$`shared"})
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 {
		t.Fatalf("prepared configs=%d, want both raw records exactly once", len(configs))
	}
	wantServers := []string{"first.example.com", "second.example.com"}
	for index, wantServer := range wantServers {
		if server, _ := stringMapValue(configs[index], "server"); server != wantServer {
			t.Fatalf("prepared config %d server=%q, want %q", index, server, wantServer)
		}
	}
}

func TestCompileProviderRegexpsSetsFiniteTimeout(t *testing.T) {
	regexps, err := compileProviderRegexps("^node$", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(regexps) != 1 {
		t.Fatalf("compiled regexps=%d, want 1", len(regexps))
	}
	if timeout := regexps[0].MatchTimeout; timeout <= 0 || timeout > time.Second {
		t.Fatalf("Provider regex timeout=%v, want a finite limit no greater than one second", timeout)
	}
}

func TestProviderRegexTimeoutsFailClosedWithoutLeakingNodeName(t *testing.T) {
	const pathologicalPattern = `(.+)*\?`
	const secretNodeName = "Do you think you found the provider-timeout-node-secret problem string!"
	proxyConfigs := []map[string]any{{
		"name": secretNodeName, "type": "trojan", "server": "timeout.example.com",
	}}
	tests := []struct {
		name           string
		providerConfig map[string]any
	}{
		{name: "filter", providerConfig: map[string]any{"filter": pathologicalPattern}},
		{name: "exclude-filter", providerConfig: map[string]any{"exclude-filter": pathologicalPattern}},
		{name: "proxy-name override", providerConfig: map[string]any{
			"override": map[string]any{
				"proxy-name": []any{map[string]any{
					"pattern": pathologicalPattern,
					"target":  "renamed",
				}},
			},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			configs, err := prepareProviderProxyConfigs(proxyConfigs, test.providerConfig)
			elapsed := time.Since(started)
			if err == nil {
				t.Fatalf("pathological Provider regex unexpectedly succeeded: %#v", configs)
			}
			if configs != nil {
				t.Fatalf("regex timeout returned partial Provider configs: %#v", configs)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "timeout") {
				t.Fatalf("regex timeout error=%q, want a timeout classification", err)
			}
			if strings.Contains(err.Error(), secretNodeName) {
				t.Fatalf("regex timeout leaked the node name: %v", err)
			}
			if elapsed > 2*time.Second {
				t.Fatalf("regex timeout took %v, want a bounded failure", elapsed)
			}
		})
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

func TestLoadProxiesPreservesTopLevelProviderDisplayNameCollision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "collision.yaml")
	config := []byte(`proxy-providers:
  p:
    type: inline
    payload:
      - name: node
        type: trojan
        server: provider.example.com
        port: 443
        password: provider-password
proxies:
  - name: "[p] node"
    type: trojan
    server: top-level.example.com
    port: 443
    password: top-level-password
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}

	tester, err := New(&Config{
		ConfigPaths: configPath,
		FilterRegex: ".*",
		ServerURL:   "https://example.com/file.bin",
		Mode:        SpeedModeFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatalf("display-name collision returned an error instead of preserving both nodes: %v", err)
	}
	if len(proxies) != 2 {
		servers := make(map[string]string, len(proxies))
		for name, proxy := range proxies {
			server, _ := stringMapValue(proxy.Config, "server")
			servers[name] = server
		}
		t.Fatalf("display-name collision silently lost a valid config before unique naming: got %d nodes, want 2; retained=%v", len(proxies), servers)
	}
	wantServers := map[string]string{
		"[p] node":     "top-level.example.com",
		"[p] node [2]": "provider.example.com",
	}
	for name, wantServer := range wantServers {
		proxy := proxies[name]
		if proxy == nil {
			t.Fatalf("missing uniquely named node %q: %#v", name, proxies)
		}
		if server, _ := stringMapValue(proxy.Config, "server"); server != wantServer {
			t.Fatalf("node %q server=%q, want %q", name, server, wantServer)
		}
		if configName, _ := stringMapValue(proxy.Config, "name"); configName != name {
			t.Fatalf("node %q config name=%q", name, configName)
		}
	}
}

func TestLoadProxiesPreservesProviderOverrideNameCollision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "override-collision.yaml")
	config := []byte(`proxy-providers:
  p:
    type: inline
    override:
      proxy-name:
        - pattern: "^(first|second)$"
          target: merged
    payload:
      - name: first
        type: trojan
        server: first.example.com
        port: 443
        password: first-password
      - name: second
        type: trojan
        server: second.example.com
        port: 443
        password: second-password
proxies: []
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}

	tester, err := New(&Config{
		ConfigPaths: configPath,
		FilterRegex: ".*",
		ServerURL:   "https://example.com/file.bin",
		Mode:        SpeedModeFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatalf("override collision returned an error instead of preserving both nodes: %v", err)
	}
	if len(proxies) != 2 {
		servers := make(map[string]string, len(proxies))
		for name, proxy := range proxies {
			server, _ := stringMapValue(proxy.Config, "server")
			servers[name] = server
		}
		t.Fatalf("override collision silently lost a valid config before unique naming: got %d nodes, want 2; retained=%v", len(proxies), servers)
	}
	wantServers := map[string]string{
		"[p] merged":     "first.example.com",
		"[p] merged [2]": "second.example.com",
	}
	for name, wantServer := range wantServers {
		proxy := proxies[name]
		if proxy == nil {
			t.Fatalf("missing override-colliding node %q: %#v", name, proxies)
		}
		if server, _ := stringMapValue(proxy.Config, "server"); server != wantServer {
			t.Fatalf("node %q server=%q, want %q", name, server, wantServer)
		}
	}
}

func TestLoadProxiesPreservesRawDuplicateNamesWithinProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "raw-provider-duplicate.yaml")
	config := []byte(`proxy-providers:
  p:
    type: inline
    payload:
      - name: shared
        type: trojan
        server: first.example.com
        port: 443
        password: first-password
      - name: shared
        type: trojan
        server: second.example.com
        port: 443
        password: second-password
proxies: []
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}

	tester, err := New(&Config{
		ConfigPaths: configPath,
		FilterRegex: ".*",
		ServerURL:   "https://example.com/file.bin",
		Mode:        SpeedModeFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatalf("raw Provider name collision returned an error instead of preserving both nodes: %v", err)
	}
	if len(proxies) != 2 {
		servers := make(map[string]string, len(proxies))
		for name, proxy := range proxies {
			server, _ := stringMapValue(proxy.Config, "server")
			servers[name] = server
		}
		t.Fatalf("raw Provider duplicate name silently lost a valid config: got %d nodes, want 2; retained=%v", len(proxies), servers)
	}
	wantServers := map[string]string{
		"[p] shared":     "first.example.com",
		"[p] shared [2]": "second.example.com",
	}
	for name, wantServer := range wantServers {
		proxy := proxies[name]
		if proxy == nil {
			t.Fatalf("missing raw Provider duplicate %q: %#v", name, proxies)
		}
		if server, _ := stringMapValue(proxy.Config, "server"); server != wantServer {
			t.Fatalf("node %q server=%q, want %q", name, server, wantServer)
		}
		if configName, _ := stringMapValue(proxy.Config, "name"); configName != name {
			t.Fatalf("node %q config name=%q", name, configName)
		}
	}
}

func TestLoadProxiesPreservesMultiSourceCollisionAndReservedSuffixes(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.yaml")
	secondPath := filepath.Join(directory, "second.yaml")
	first := []byte(`proxies:
  - name: shared
    type: trojan
    server: first.example.com
    port: 443
    password: first-password
`)
	second := []byte(`proxy-providers:
  p:
    type: inline
    payload:
      - name: shared
        type: trojan
        server: provider.example.com
        port: 443
        password: provider-password
proxies:
  - name: shared
    type: trojan
    server: second.example.com
    port: 443
    password: second-password
  - name: "shared [2]"
    type: trojan
    server: reserved-two.example.com
    port: 443
    password: reserved-two-password
  - name: "shared [3]"
    type: trojan
    server: reserved-three.example.com
    port: 443
    password: reserved-three-password
`)
	if err := os.WriteFile(firstPath, first, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, second, 0o600); err != nil {
		t.Fatal(err)
	}

	load := func() map[string]*CProxy {
		t.Helper()
		tester, err := New(&Config{
			ConfigPaths: firstPath + "," + secondPath,
			FilterRegex: ".*",
			ServerURL:   "https://example.com/file.bin",
			Mode:        SpeedModeFast,
		})
		if err != nil {
			t.Fatal(err)
		}
		proxies, err := tester.LoadProxies()
		if err != nil {
			t.Fatal(err)
		}
		return proxies
	}
	firstLoad := load()
	secondLoad := load()
	if len(firstLoad) != 5 {
		t.Fatalf("multi-source merge retained %d nodes, want 5: %#v", len(firstLoad), firstLoad)
	}
	wantServers := map[string]string{
		"shared":     "first.example.com",
		"shared [2]": "reserved-two.example.com",
		"shared [3]": "reserved-three.example.com",
		"shared [4]": "second.example.com",
		"[p] shared": "provider.example.com",
	}
	for name, wantServer := range wantServers {
		for loadIndex, proxies := range []map[string]*CProxy{firstLoad, secondLoad} {
			proxy := proxies[name]
			if proxy == nil {
				t.Fatalf("load %d missing deterministic name %q: %#v", loadIndex+1, name, proxies)
			}
			if server, _ := stringMapValue(proxy.Config, "server"); server != wantServer {
				t.Fatalf("load %d node %q server=%q, want %q", loadIndex+1, name, server, wantServer)
			}
		}
	}
}

func TestLoadProxiesRejectsTopLevelDialerProxyBeforeProbe(t *testing.T) {
	var targetRequests atomic.Int32
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer targetServer.Close()

	configPath := filepath.Join(t.TempDir(), "dialer-proxy.yaml")
	config := []byte(`proxies:
  - name: base
    type: socks5
    server: 127.0.0.1
    port: 1080
  - name: chained
    type: trojan
    server: example.com
    port: 443
    password: DO-NOT-LEAK-TOP-LEVEL-PASSWORD
    dialer-proxy: base
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	tester, err := New(&Config{
		ConfigPaths:  configPath,
		FilterRegex:  ".*",
		ServerURL:    targetServer.URL,
		Mode:         SpeedModeFast,
		ProbeTimeout: 100 * time.Millisecond,
		Concurrent:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, loadErr := tester.LoadProxies()
	if loadErr == nil {
		chained := proxies["chained"]
		if chained == nil {
			t.Fatalf("dialer-proxy config loaded without its chained node: %#v", proxies)
		}
		conn, dialErr := chained.DialContext(context.Background(), &C.Metadata{
			NetWork: C.TCP,
			Host:    "probe.example.com",
			DstPort: 443,
		})
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("dialer-proxy config was accepted as a normal node batch; subsequent dial error=%v", dialErr)
	}
	if proxies != nil {
		t.Fatalf("rejected dialer-proxy batch returned partial proxies: %#v", proxies)
	}
	if !strings.Contains(loadErr.Error(), "dialer-proxy") || !strings.Contains(loadErr.Error(), "chained") {
		t.Fatalf("dialer-proxy rejection lacks safe node location: %v", loadErr)
	}
	if strings.Contains(loadErr.Error(), "DO-NOT-LEAK-TOP-LEVEL-PASSWORD") {
		t.Fatalf("dialer-proxy rejection leaked connection credentials: %v", loadErr)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("dialer-proxy rejection reached the probe target %d times", got)
	}
}

func TestLoadProxiesRejectsProviderDialerProxyBatch(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "provider-dialer-proxy.yaml")
	config := []byte(`proxy-providers:
  chained-provider:
    type: inline
    payload:
      - name: chained-node
        type: trojan
        server: chained.example.com
        port: 443
        password: DO-NOT-LEAK-DIRECT-LOAD-PASSWORD
        dialer-proxy: ordinary-node
proxies:
  - name: ordinary-node
    type: trojan
    server: ordinary.example.com
    port: 443
    password: ordinary-password
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	tester, err := New(&Config{
		ConfigPaths: configPath,
		FilterRegex: ".*",
		ServerURL:   "https://example.com/file.bin",
		Mode:        SpeedModeFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err == nil || !strings.Contains(err.Error(), "dialer-proxy") ||
		!strings.Contains(err.Error(), "chained-provider") || !strings.Contains(err.Error(), "chained-node") {
		t.Fatalf("direct Provider load did not reject dialer-proxy clearly: proxies=%#v err=%v", proxies, err)
	}
	if proxies != nil {
		t.Fatalf("direct Provider load returned ordinary nodes from a rejected batch: %#v", proxies)
	}
	if strings.Contains(err.Error(), "DO-NOT-LEAK-DIRECT-LOAD-PASSWORD") {
		t.Fatalf("direct Provider rejection leaked credentials: %v", err)
	}
}

func TestLoadProxiesAllowsEmptyDialerProxy(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "empty-dialer-proxy.yaml")
	config := []byte(`proxy-providers:
  empty-provider:
    type: inline
    override:
      dialer-proxy: ""
    payload:
      - name: provider-node
        type: trojan
        server: provider.example.com
        port: 443
        password: provider-password
        dialer-proxy: ""
proxies:
  - name: top-level-node
    type: trojan
    server: top-level.example.com
    port: 443
    password: top-level-password
    dialer-proxy: ""
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	tester, err := New(&Config{
		ConfigPaths: configPath,
		FilterRegex: ".*",
		ServerURL:   "https://example.com/file.bin",
		Mode:        SpeedModeFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatalf("empty dialer-proxy must not be treated as a dependency: %v", err)
	}
	if len(proxies) != 2 {
		t.Fatalf("empty dialer-proxy config loaded %d nodes, want 2", len(proxies))
	}
}

func TestPrepareConfigSourcesRejectsDialerProxyProviders(t *testing.T) {
	const secret = "DO-NOT-LEAK-PROVIDER-PASSWORD"
	providerBody := `proxies:
  - name: chained-provider-node
    type: trojan
    server: provider.example.com
    port: 443
    password: ` + secret + `
    dialer-proxy: base
`

	tests := []struct {
		name             string
		buildConfig      func(t *testing.T, directory string) (string, *atomic.Int32)
		wantProviderName string
		wantNodeName     string
		wantFetches      int32
	}{
		{
			name: "inline provider node",
			buildConfig: func(t *testing.T, directory string) (string, *atomic.Int32) {
				path := filepath.Join(directory, "inline.yaml")
				body := `proxy-providers:
  inline-chain:
    type: inline
    payload:
      - name: chained-provider-node
        type: trojan
        server: provider.example.com
        port: 443
        password: ` + secret + `
        dialer-proxy: base
proxies: []
`
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				return path, nil
			},
			wantProviderName: "inline-chain",
			wantNodeName:     "chained-provider-node",
		},
		{
			name: "file provider node",
			buildConfig: func(t *testing.T, directory string) (string, *atomic.Int32) {
				providerPath := filepath.Join(directory, "nodes.yaml")
				if err := os.WriteFile(providerPath, []byte(providerBody), 0o600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(directory, "file.yaml")
				body := `proxy-providers:
  file-chain:
    type: file
    path: nodes.yaml
proxies: []
`
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				return path, nil
			},
			wantProviderName: "file-chain",
			wantNodeName:     "chained-provider-node",
		},
		{
			name: "HTTP provider node",
			buildConfig: func(t *testing.T, directory string) (string, *atomic.Int32) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					_, _ = io.WriteString(w, providerBody)
				}))
				t.Cleanup(server.Close)
				path := filepath.Join(directory, "http.yaml")
				body := fmt.Sprintf(`proxy-providers:
  http-chain:
    type: http
    url: %q
proxies: []
`, server.URL)
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				return path, &requests
			},
			wantProviderName: "http-chain",
			wantNodeName:     "chained-provider-node",
			wantFetches:      1,
		},
		{
			name: "provider override",
			buildConfig: func(t *testing.T, directory string) (string, *atomic.Int32) {
				path := filepath.Join(directory, "override.yaml")
				body := `proxy-providers:
  override-chain:
    type: inline
    override:
      dialer-proxy: base
    payload:
      - name: override-node
        type: trojan
        server: provider.example.com
        port: 443
        password: ` + secret + `
proxies: []
`
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				return path, nil
			},
			wantProviderName: "override-chain",
			wantNodeName:     "override-node",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			oldOutputPath := filepath.Join(directory, "existing-output.yaml")
			const oldOutput = "existing-output-must-survive\n"
			if err := os.WriteFile(oldOutputPath, []byte(oldOutput), 0o600); err != nil {
				t.Fatal(err)
			}
			configPath, requests := test.buildConfig(t, directory)
			preparedDirectory := filepath.Join(directory, "prepared")
			result, err := PrepareConfigSources([]ConfigSource{{
				Path: configPath, Origin: SourceOriginLocal, LocalDependency: configPath,
			}}, preparedDirectory, "")
			if err == nil {
				t.Fatalf("dialer-proxy Provider unexpectedly materialized successfully: %#v", result)
			}
			if result != nil {
				t.Fatalf("dialer-proxy Provider returned a partial result: %#v", result)
			}
			if !strings.Contains(err.Error(), "dialer-proxy") ||
				!strings.Contains(err.Error(), test.wantProviderName) ||
				!strings.Contains(err.Error(), test.wantNodeName) {
				t.Fatalf("dialer-proxy Provider rejection lacks safe location: %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("dialer-proxy Provider rejection leaked connection credentials: %v", err)
			}
			if requests != nil && requests.Load() != test.wantFetches {
				t.Fatalf("HTTP Provider fetches=%d, want %d", requests.Load(), test.wantFetches)
			}
			if _, statErr := os.Stat(filepath.Join(preparedDirectory, "materialized-001.yaml")); !os.IsNotExist(statErr) {
				t.Fatalf("dialer-proxy Provider left materialized output: %v", statErr)
			}
			preserved, readErr := os.ReadFile(oldOutputPath)
			if readErr != nil || string(preserved) != oldOutput {
				t.Fatalf("dialer-proxy Provider changed old output: content=%q err=%v", preserved, readErr)
			}
		})
	}
}

func TestPrepareConfigSourcesDialerProxyFailureCleansEarlierMaterialization(t *testing.T) {
	directory := t.TempDir()
	oldOutputPath := filepath.Join(directory, "existing-output.yaml")
	const oldOutput = "existing-output-must-survive\n"
	if err := os.WriteFile(oldOutputPath, []byte(oldOutput), 0o600); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(directory, "first.yaml")
	if err := os.WriteFile(firstPath, []byte(`proxies:
  - name: ordinary-node
    type: trojan
    server: ordinary.example.com
    port: 443
    password: ordinary-password
`), 0o600); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(directory, "second.yaml")
	if err := os.WriteFile(secondPath, []byte(`proxies:
  - name: rejected-chain
    type: trojan
    server: chain.example.com
    port: 443
    password: DO-NOT-LEAK-MIXED-PASSWORD
    dialer-proxy: ordinary-node
`), 0o600); err != nil {
		t.Fatal(err)
	}
	preparedDirectory := filepath.Join(directory, "prepared")
	result, err := PrepareConfigSources([]ConfigSource{
		{Path: firstPath, Origin: SourceOriginLocal, LocalDependency: firstPath},
		{Path: secondPath, Origin: SourceOriginLocal, LocalDependency: secondPath},
	}, preparedDirectory, "")
	if err == nil || !strings.Contains(err.Error(), "dialer-proxy") || !strings.Contains(err.Error(), "rejected-chain") {
		t.Fatalf("mixed dialer-proxy batch did not fail clearly: result=%#v err=%v", result, err)
	}
	if result != nil {
		t.Fatalf("mixed dialer-proxy batch returned a partial result: %#v", result)
	}
	if strings.Contains(err.Error(), "DO-NOT-LEAK-MIXED-PASSWORD") {
		t.Fatalf("mixed dialer-proxy rejection leaked credentials: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(preparedDirectory, "materialized-001.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("mixed dialer-proxy failure left earlier materialization: %v", statErr)
	}
	preserved, readErr := os.ReadFile(oldOutputPath)
	if readErr != nil || string(preserved) != oldOutput {
		t.Fatalf("mixed dialer-proxy failure changed old output: content=%q err=%v", preserved, readErr)
	}
}

func TestLoadProxiesLoadsRelativeFileProviderPayload(t *testing.T) {
	directory := t.TempDir()
	providerDirectory := filepath.Join(directory, "providers")
	if err := os.MkdirAll(providerDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	providerPath := filepath.Join(providerDirectory, "nodes.yaml")
	if err := os.WriteFile(providerPath, []byte(`payload:
  - name: local-node
    type: trojan
    server: local.example.com
    port: 443
    password: local-password
    sni: local.example.com
`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`proxy-providers:
  local:
    type: file
    path: providers/nodes.yaml
proxies: []
`), 0o600); err != nil {
		t.Fatal(err)
	}

	tester, err := New(&Config{
		ConfigPaths: configPath, FilterRegex: ".*", ServerURL: "https://example.com/file.bin",
		Mode: SpeedModeFast, ProbeTimeout: time.Second, Concurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatal(err)
	}
	loaded := proxies["[local] local-node"]
	if loaded == nil {
		t.Fatalf("local file provider proxy is missing: %#v", proxies)
	}
	if got, _ := stringMapValue(loaded.Config, "server"); got != "local.example.com" {
		t.Fatalf("provider server=%q, want local.example.com", got)
	}
}

func TestLoadProxiesFailsClosedWhenProviderCannotBeFetched(t *testing.T) {
	var requests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "provider-response-secret", http.StatusBadGateway)
	}))
	defer providerServer.Close()

	providerURL := providerServer.URL + "/nodes?token=provider-token-secret"
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configBody := fmt.Sprintf(`proxy-providers:
  unavailable:
    type: http
    url: %q
proxies:
  - name: direct-node
    type: trojan
    server: direct.example.com
    port: 443
    password: direct-password
`, providerURL)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	tester, err := New(&Config{
		ConfigPaths: configPath, FilterRegex: ".*", ServerURL: "https://example.com/file.bin",
		Mode: SpeedModeFast, ProbeTimeout: time.Second, Concurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err == nil || !strings.Contains(err.Error(), "unavailable") ||
		!strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("provider failure must fail the whole load, got proxies=%#v err=%v", proxies, err)
	}
	if proxies != nil {
		t.Fatalf("partial proxy set must not be returned: %#v", proxies)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("failed Provider requests=%d, want exactly one", got)
	}
	for _, secret := range []string{providerURL, "provider-token-secret", "provider-response-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider error leaked %q: %v", secret, err)
		}
	}
}

func TestPrepareConfigSourcesRejectsRemoteHTTPProviderPrivateNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `payload:
  - name: private-http-node
    type: trojan
    server: node.example.com
    port: 443
    password: provider-password
`)
	}))
	defer server.Close()

	providerURLs := []struct {
		name string
		url  string
	}{
		{name: "loopback IP", url: server.URL},
		{name: "localhost", url: strings.Replace(server.URL, "127.0.0.1", "localhost", 1)},
	}
	for _, test := range providerURLs {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "remote-config.yaml")
			configBody := fmt.Sprintf(`proxy-providers:
  private-http:
    type: http
    url: %q
proxies: []`, test.url)
			if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
				t.Fatal(err)
			}

			result, err := PrepareConfigSources([]ConfigSource{{
				Path: configPath, Origin: SourceOriginRemote,
			}}, directory, "")
			if err == nil || !strings.Contains(err.Error(), "private or local network") {
				t.Fatalf("remote Provider private-network request must fail, result=%#v err=%v", result, err)
			}
			if result != nil {
				t.Fatalf("blocked remote Provider returned a partial result: %#v", result)
			}
			if _, statErr := os.Stat(filepath.Join(directory, "materialized-001.yaml")); !os.IsNotExist(statErr) {
				t.Fatalf("blocked remote Provider left materialized output: %v", statErr)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("blocked remote Providers reached the private server %d times", got)
	}
}

func TestRestrictedProviderDialerRejectsNonPublicResolvedAddresses(t *testing.T) {
	tests := []struct {
		name    string
		address netip.Addr
	}{
		{name: "loopback", address: netip.MustParseAddr("127.0.0.1")},
		{name: "private IPv4", address: netip.MustParseAddr("10.1.2.3")},
		{name: "carrier grade NAT", address: netip.MustParseAddr("100.64.0.1")},
		{name: "link-local IPv4", address: netip.MustParseAddr("169.254.1.2")},
		{name: "loopback IPv6", address: netip.MustParseAddr("::1")},
		{name: "private IPv6", address: netip.MustParseAddr("fd00::1")},
		{name: "link-local IPv6", address: netip.MustParseAddr("fe80::1")},
		{name: "local-use NAT64", address: netip.MustParseAddr("64:ff9b:1::1")},
		{name: "dummy IPv6", address: netip.MustParseAddr("100:0:0:1::1")},
		{name: "IPv6 documentation", address: netip.MustParseAddr("3fff::1")},
		{name: "SRv6 SID", address: netip.MustParseAddr("5f00::1")},
		{name: "6to4", address: netip.MustParseAddr("2002::1")},
		{name: "6a44 relay", address: netip.MustParseAddr("192.88.99.2")},
		{name: "deprecated 6to4 relay block", address: netip.MustParseAddr("192.88.99.1")},
		{name: "TEREDO indeterminate", address: netip.MustParseAddr("2001::1")},
		{name: "deprecated ORCHID", address: netip.MustParseAddr("2001:10::1")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dialCalls atomic.Int32
			dialer := &restrictedProviderDialer{
				lookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{test.address}, nil
				},
				dialContext: func(context.Context, string, string) (net.Conn, error) {
					dialCalls.Add(1)
					return nil, fmt.Errorf("unexpected dial")
				},
			}
			_, err := dialer.DialContext(context.Background(), "tcp", "provider.test:80")
			if err == nil || !strings.Contains(err.Error(), "private or local network") {
				t.Fatalf("non-public address was not blocked: %v", err)
			}
			if got := dialCalls.Load(); got != 0 {
				t.Fatalf("non-public address reached the underlying dialer %d times", got)
			}
		})
	}
}

func TestRestrictedProviderDialerAllowsIANAReachableAddresses(t *testing.T) {
	tests := []struct {
		name    string
		address netip.Addr
	}{
		{name: "ordinary public IPv4", address: netip.MustParseAddr("8.8.8.8")},
		{name: "ordinary public IPv6", address: netip.MustParseAddr("2606:4700:4700::1111")},
		{name: "public IPv4-mapped IPv6", address: netip.MustParseAddr("::ffff:8.8.4.4")},
		{name: "PCP anycast override", address: netip.MustParseAddr("192.0.0.9")},
		{name: "IPv6 PCP anycast override", address: netip.MustParseAddr("2001:1::1")},
		{name: "AMT override", address: netip.MustParseAddr("2001:3::1")},
		{name: "AS112 override", address: netip.MustParseAddr("2001:4:112::1")},
		{name: "ORCHIDv2 override", address: netip.MustParseAddr("2001:20::1")},
		{name: "DET override", address: netip.MustParseAddr("2001:30::1")},
		{name: "well-known NAT64", address: netip.MustParseAddr("64:ff9b::808:808")},
		{name: "IPv4 AS112", address: netip.MustParseAddr("192.31.196.1")},
		{name: "IPv4 AMT", address: netip.MustParseAddr("192.52.193.1")},
		{name: "IPv4 direct AS112", address: netip.MustParseAddr("192.175.48.1")},
		{name: "IPv6 direct AS112", address: netip.MustParseAddr("2620:4f:8000::1")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dialCalls atomic.Int32
			var dialedAddress string
			dialer := &restrictedProviderDialer{
				lookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{test.address}, nil
				},
				dialContext: func(_ context.Context, _ string, address string) (net.Conn, error) {
					dialCalls.Add(1)
					dialedAddress = address
					client, server := net.Pipe()
					_ = server.Close()
					return client, nil
				},
			}
			conn, err := dialer.DialContext(context.Background(), "tcp", "provider.test:443")
			if err != nil {
				t.Fatalf("IANA-reachable address was blocked: %v", err)
			}
			_ = conn.Close()
			if got := dialCalls.Load(); got != 1 {
				t.Fatalf("IANA-reachable address underlying dials=%d, want one", got)
			}
			wantAddress := net.JoinHostPort(test.address.Unmap().String(), "443")
			if dialedAddress != wantAddress {
				t.Fatalf("underlying dial address=%q, want validated literal %q", dialedAddress, wantAddress)
			}
		})
	}
}

func TestRemoteProviderRevalidatesRedirectDestination(t *testing.T) {
	var privateRequests atomic.Int32
	privateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		privateRequests.Add(1)
		_, _ = io.WriteString(w, "payload: []")
	}))
	defer privateServer.Close()

	var initialRequests atomic.Int32
	initialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initialRequests.Add(1)
		http.Redirect(w, r, privateServer.URL+"/provider", http.StatusFound)
	}))
	defer initialServer.Close()
	_, initialPort, err := net.SplitHostPort(initialServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	publicAddress := netip.MustParseAddr("93.184.216.34")
	dialer := &restrictedProviderDialer{
		lookupNetIP: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			if host != "public-provider.test" {
				return nil, fmt.Errorf("unexpected lookup for %s", host)
			}
			return []netip.Addr{publicAddress}, nil
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != net.JoinHostPort(publicAddress.String(), initialPort) {
				return nil, fmt.Errorf("unexpected underlying destination %s", address)
			}
			var systemDialer net.Dialer
			return systemDialer.DialContext(ctx, network, initialServer.Listener.Addr().String())
		},
	}
	tester := &SpeedTester{config: &Config{}, remoteProviderDialer: dialer}
	_, _, err = tester.loadProviderProxyConfigsFromSource(
		ConfigSource{Origin: SourceOriginRemote},
		"redirected",
		map[string]any{
			"type": "http",
			"url":  "http://public-provider.test:" + initialPort + "/provider",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "private or local network") {
		t.Fatalf("redirect to private address was not blocked: %v", err)
	}
	if got := initialRequests.Load(); got != 1 {
		t.Fatalf("initial public endpoint requests=%d, want one", got)
	}
	if got := privateRequests.Load(); got != 0 {
		t.Fatalf("redirected private endpoint requests=%d, want zero", got)
	}
}

func TestRemoteProviderRevalidatesDNSOnRedirect(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/first" {
			w.Header().Set("Connection", "close")
			http.Redirect(w, r, "/second", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `payload:
  - name: rebound-node
    type: trojan
    server: node.example.com
    port: 443
    password: provider-password
`)
	}))
	defer server.Close()
	_, serverPort, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	publicAddress := netip.MustParseAddr("93.184.216.34")
	var lookups atomic.Int32
	var dials atomic.Int32
	dialer := &restrictedProviderDialer{
		lookupNetIP: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			if host != "rebind-provider.test" {
				return nil, fmt.Errorf("unexpected lookup for %s", host)
			}
			if lookups.Add(1) == 1 {
				return []netip.Addr{publicAddress}, nil
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials.Add(1)
			if address != net.JoinHostPort(publicAddress.String(), serverPort) {
				return nil, fmt.Errorf("unexpected underlying destination %s", address)
			}
			var systemDialer net.Dialer
			return systemDialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	tester := &SpeedTester{config: &Config{}, remoteProviderDialer: dialer}
	_, _, err = tester.loadProviderProxyConfigsFromSource(
		ConfigSource{Origin: SourceOriginRemote},
		"rebound",
		map[string]any{
			"type": "http",
			"url":  "http://rebind-provider.test:" + serverPort + "/first",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "private or local network") {
		t.Fatalf("DNS rebinding redirect was not blocked: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("DNS-rebinding server requests=%d, want only the first request", got)
	}
	if got := lookups.Load(); got != 2 {
		t.Fatalf("redirect DNS lookups=%d, want one lookup per connection", got)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("underlying public dials=%d, want only the validated first dial", got)
	}
}

const redirectProviderBody = `payload:
  - name: redirect-node
    type: trojan
    server: node.example.com
    port: 443
    password: provider-password
`

func TestProviderCredentialedCrossOriginRedirectIsBlockedBeforeRequest(t *testing.T) {
	var secondRequests atomic.Int32
	var secondAuthorization string
	var secondCookie string
	var secondToken string
	var secondReferer string
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		secondAuthorization = r.Header.Get("Authorization")
		secondCookie = r.Header.Get("Cookie")
		secondToken = r.Header.Get("X-Provider-Token")
		secondReferer = r.Header.Get("Referer")
		_, _ = io.WriteString(w, redirectProviderBody)
	}))
	defer secondServer.Close()
	_, secondPort, err := net.SplitHostPort(secondServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	var firstRequests atomic.Int32
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-authorization" ||
			r.Header.Get("Cookie") != "provider_session=test-session" ||
			r.Header.Get("X-Provider-Token") != "test-provider-token" {
			http.Error(w, "missing test credentials", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r,
			"http://provider-b.test:"+secondPort+"/nodes", http.StatusFound)
	}))
	defer firstServer.Close()
	_, firstPort, err := net.SplitHostPort(firstServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	firstIP := netip.MustParseAddr("93.184.216.34")
	secondIP := netip.MustParseAddr("1.1.1.1")
	dialer := &restrictedProviderDialer{
		lookupNetIP: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			switch host {
			case "provider-a.test":
				return []netip.Addr{firstIP}, nil
			case "provider-b.test":
				return []netip.Addr{secondIP}, nil
			default:
				return nil, fmt.Errorf("unexpected lookup for %s", host)
			}
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			var target string
			switch address {
			case net.JoinHostPort(firstIP.String(), firstPort):
				target = firstServer.Listener.Addr().String()
			case net.JoinHostPort(secondIP.String(), secondPort):
				target = secondServer.Listener.Addr().String()
			default:
				return nil, fmt.Errorf("unexpected underlying destination %s", address)
			}
			var systemDialer net.Dialer
			return systemDialer.DialContext(ctx, network, target)
		},
	}
	tester := &SpeedTester{config: &Config{}, remoteProviderDialer: dialer}
	_, _, err = tester.loadProviderProxyConfigsFromSource(
		ConfigSource{Origin: SourceOriginRemote},
		"credentialed-redirect",
		map[string]any{
			"type": "http",
			"url": "http://provider-a.test:" + firstPort +
				"/start?access_token=test-query-token",
			"header": map[string]any{
				"Authorization":    "Bearer test-authorization",
				"Cookie":           "provider_session=test-session",
				"X-Provider-Token": "test-provider-token",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "redirect blocked") {
		t.Errorf("credentialed cross-origin redirect must fail before the second request: %v", err)
	}
	if got := firstRequests.Load(); got != 1 {
		t.Fatalf("credentialed first endpoint requests=%d, want one", got)
	}
	if got := secondRequests.Load(); got != 0 {
		t.Fatalf(
			"credentialed redirect reached second endpoint %d times: Authorization=%q Cookie=%q Token=%q Referer=%q",
			got, secondAuthorization, secondCookie, secondToken, secondReferer)
	}
}

func TestProviderUnauthenticatedCrossOriginRedirectClearsReferer(t *testing.T) {
	var secondRequests atomic.Int32
	var secondReferer string
	var secondUserAgent string
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		secondReferer = r.Header.Get("Referer")
		secondUserAgent = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, redirectProviderBody)
	}))
	defer secondServer.Close()
	_, secondPort, err := net.SplitHostPort(secondServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r,
			"http://cdn-provider.test:"+secondPort+"/nodes", http.StatusFound)
	}))
	defer firstServer.Close()
	_, firstPort, err := net.SplitHostPort(firstServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	firstIP := netip.MustParseAddr("93.184.216.34")
	secondIP := netip.MustParseAddr("1.1.1.1")
	var dialCalls atomic.Int32
	dialer := &restrictedProviderDialer{
		lookupNetIP: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			switch host {
			case "origin-provider.test":
				return []netip.Addr{firstIP}, nil
			case "cdn-provider.test":
				return []netip.Addr{secondIP}, nil
			default:
				return nil, fmt.Errorf("unexpected lookup for %s", host)
			}
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialCalls.Add(1)
			var target string
			switch address {
			case net.JoinHostPort(firstIP.String(), firstPort):
				target = firstServer.Listener.Addr().String()
			case net.JoinHostPort(secondIP.String(), secondPort):
				target = secondServer.Listener.Addr().String()
			default:
				return nil, fmt.Errorf("unexpected underlying destination %s", address)
			}
			var systemDialer net.Dialer
			return systemDialer.DialContext(ctx, network, target)
		},
	}
	tester := &SpeedTester{config: &Config{}, remoteProviderDialer: dialer}
	providerConfigs, _, err := tester.loadProviderProxyConfigsFromSource(
		ConfigSource{Origin: SourceOriginRemote},
		"cdn-redirect",
		map[string]any{
			"type": "http",
			"url":  "http://origin-provider.test:" + firstPort + "/start",
		},
	)
	if err != nil || len(providerConfigs) != 1 {
		t.Fatalf("unauthenticated CDN redirect failed: configs=%#v err=%v", providerConfigs, err)
	}
	if got := secondRequests.Load(); got != 1 {
		t.Fatalf("unauthenticated CDN endpoint requests=%d, want one", got)
	}
	if secondReferer != "" {
		t.Fatalf("unauthenticated cross-origin redirect leaked Referer %q", secondReferer)
	}
	if secondUserAgent == "" {
		t.Fatal("application User-Agent was lost after clearing redirected user headers")
	}
	if got := dialCalls.Load(); got != 2 {
		t.Fatalf("cross-origin redirect validated dials=%d, want two", got)
	}
}

func TestProviderRejectsHTTPSDowngradeBeforeSecondRequest(t *testing.T) {
	var secondRequests atomic.Int32
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondRequests.Add(1)
		_, _ = io.WriteString(w, redirectProviderBody)
	}))
	defer secondServer.Close()
	_, secondPort, err := net.SplitHostPort(secondServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	firstServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r,
			"http://downgrade-target.test:"+secondPort+"/nodes", http.StatusFound)
	}))
	defer firstServer.Close()
	if err := mihomoCA.AddCertificate(string(firstServer.Certificate().Raw)); err != nil {
		t.Fatal(err)
	}
	defer mihomoCA.ResetCertificate()
	if len(firstServer.Certificate().DNSNames) == 0 {
		t.Fatal("test TLS certificate contains no DNS name")
	}
	firstHost := firstServer.Certificate().DNSNames[0]
	_, firstPort, err := net.SplitHostPort(firstServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	firstIP := netip.MustParseAddr("93.184.216.34")
	secondIP := netip.MustParseAddr("1.1.1.1")
	dialer := &restrictedProviderDialer{
		lookupNetIP: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			switch host {
			case firstHost:
				return []netip.Addr{firstIP}, nil
			case "downgrade-target.test":
				return []netip.Addr{secondIP}, nil
			default:
				return nil, fmt.Errorf("unexpected lookup for %s", host)
			}
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			var target string
			switch address {
			case net.JoinHostPort(firstIP.String(), firstPort):
				target = firstServer.Listener.Addr().String()
			case net.JoinHostPort(secondIP.String(), secondPort):
				target = secondServer.Listener.Addr().String()
			default:
				return nil, fmt.Errorf("unexpected underlying destination %s", address)
			}
			var systemDialer net.Dialer
			return systemDialer.DialContext(ctx, network, target)
		},
	}
	tester := &SpeedTester{config: &Config{}, remoteProviderDialer: dialer}
	_, _, err = tester.loadProviderProxyConfigsFromSource(
		ConfigSource{Origin: SourceOriginRemote},
		"downgrade",
		map[string]any{
			"type": "http",
			"url":  "https://" + net.JoinHostPort(firstHost, firstPort) + "/start",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "HTTPS to HTTP") {
		t.Fatalf("HTTPS downgrade must fail before the second request: %v", err)
	}
	if got := secondRequests.Load(); got != 0 {
		t.Fatalf("HTTPS downgrade reached the HTTP endpoint %d times", got)
	}
}

func TestProviderCredentialedSameOriginRedirectSucceeds(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer same-origin-test" ||
			r.Header.Get("Cookie") != "provider_session=same-origin" ||
			r.Header.Get("X-Provider-Token") != "same-origin-token" {
			http.Error(w, "missing same-origin credentials", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/nodes", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, redirectProviderBody)
	}))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	publicIP := netip.MustParseAddr("93.184.216.34")
	dialer := &restrictedProviderDialer{
		lookupNetIP: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			if host != "same-origin-provider.test" {
				return nil, fmt.Errorf("unexpected lookup for %s", host)
			}
			return []netip.Addr{publicIP}, nil
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != net.JoinHostPort(publicIP.String(), port) {
				return nil, fmt.Errorf("unexpected underlying destination %s", address)
			}
			var systemDialer net.Dialer
			return systemDialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	tester := &SpeedTester{config: &Config{}, remoteProviderDialer: dialer}
	providerConfigs, _, err := tester.loadProviderProxyConfigsFromSource(
		ConfigSource{Origin: SourceOriginRemote},
		"same-origin",
		map[string]any{
			"type": "http",
			"url": "http://same-origin-provider.test:" + port +
				"/start?access_token=same-origin-query",
			"header": map[string]any{
				"Authorization":    "Bearer same-origin-test",
				"Cookie":           "provider_session=same-origin",
				"X-Provider-Token": "same-origin-token",
			},
		},
	)
	if err != nil || len(providerConfigs) != 1 {
		t.Fatalf("credentialed same-origin redirect failed: configs=%#v err=%v", providerConfigs, err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("same-origin redirect requests=%d, want two", got)
	}
}

func TestProviderRedirectPolicyTreatsURLCredentialsAsStrictOrigin(t *testing.T) {
	tests := []struct {
		name        string
		initial     string
		redirect    string
		wantBlocked bool
	}{
		{
			name:        "URL userinfo cross-origin",
			initial:     "https://test-user:test-password@provider.example/nodes",
			redirect:    "https://cdn.example/nodes",
			wantBlocked: true,
		},
		{
			name:        "query cross-origin",
			initial:     "https://provider.example/nodes?access_token=test-query-token",
			redirect:    "https://cdn.example/nodes",
			wantBlocked: true,
		},
		{
			name:     "userinfo same effective origin",
			initial:  "https://test-user:test-password@provider.example/nodes",
			redirect: "https://provider.example:443/next",
		},
		{
			name:        "userinfo different effective port",
			initial:     "https://test-user:test-password@provider.example/nodes",
			redirect:    "https://provider.example:444/next",
			wantBlocked: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			initialURL, err := url.Parse(test.initial)
			if err != nil {
				t.Fatal(err)
			}
			redirectURL, err := url.Parse(test.redirect)
			if err != nil {
				t.Fatal(err)
			}
			policy := providerRedirectPolicy(initialURL, false, "test-user-agent")
			err = policy(
				&mihomoTransportHTTP.Request{
					URL: redirectURL, Header: make(mihomoTransportHTTP.Header),
				},
				[]*mihomoTransportHTTP.Request{{URL: initialURL}},
			)
			if test.wantBlocked && err == nil {
				t.Fatal("credentialed redirect policy unexpectedly allowed a non-strict origin")
			}
			if !test.wantBlocked && err != nil {
				t.Fatalf("credentialed redirect policy rejected the same effective origin: %v", err)
			}
		})
	}
}

func TestLoadProxiesFailsClosedWhenOneConfigCannotBeRead(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "valid.yaml")
	if err := os.WriteFile(validPath, []byte(`proxies:
  - name: valid-node
    type: trojan
    server: valid.example.com
    port: 443
    password: valid-password
`), 0o600); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(directory, "missing.yaml")

	tester, err := New(&Config{
		ConfigPaths: validPath + "," + missingPath, FilterRegex: ".*",
		ServerURL: "https://example.com/file.bin", Mode: SpeedModeFast,
		ProbeTimeout: time.Second, Concurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("missing config must fail the whole load, got proxies=%#v err=%v", proxies, err)
	}
	if proxies != nil {
		t.Fatalf("partial proxy set must not be returned: %#v", proxies)
	}
}

func TestPrepareConfigSourcesRejectsRemoteFileProvidersAfterTemporaryMaterialization(t *testing.T) {
	directory := t.TempDir()
	localProviderPath := filepath.Join(directory, "private-provider.yaml")
	if err := os.WriteFile(localProviderPath, []byte(`payload:
  - name: private-node
    type: trojan
    server: private.example.com
    port: 443
    password: private-password
`), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		providerPath string
	}{
		{name: "absolute path", providerPath: localProviderPath},
		{name: "parent traversal", providerPath: `../private-provider.yaml`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preparedDirectory := t.TempDir()
			preparedConfigPath := filepath.Join(preparedDirectory, "config-from-remote.yaml")
			configBody := fmt.Sprintf(`proxy-providers:
  remote-file:
    type: file
    path: %q
proxies: []
`, test.providerPath)
			if err := os.WriteFile(preparedConfigPath, []byte(configBody), 0o600); err != nil {
				t.Fatal(err)
			}

			result, err := PrepareConfigSources([]ConfigSource{{
				Path: preparedConfigPath, Origin: SourceOriginRemote,
			}}, preparedDirectory, "")
			if err == nil || !strings.Contains(err.Error(), "forbidden for remote sources") {
				t.Fatalf("remote file provider must fail before local read, result=%#v err=%v", result, err)
			}
			if result != nil {
				t.Fatalf("failed remote source returned a partial result: %#v", result)
			}
			if _, statErr := os.Stat(filepath.Join(preparedDirectory, "materialized-001.yaml")); !os.IsNotExist(statErr) {
				t.Fatalf("failed remote source left materialized output: %v", statErr)
			}
		})
	}
}

func TestLoadProxiesRejectsFileProviderFromDirectRemoteConfig(t *testing.T) {
	directory := t.TempDir()
	providerPath := filepath.Join(directory, "private-provider.yaml")
	if err := os.WriteFile(providerPath, []byte(`payload:
  - name: private-node
    type: trojan
    server: private.example.com
    port: 443
    password: private-password
`), 0o600); err != nil {
		t.Fatal(err)
	}
	configBody := fmt.Sprintf(`proxy-providers:
  private:
    type: file
    path: %q
proxies: []
`, providerPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, configBody)
	}))
	defer server.Close()

	tester, err := New(&Config{
		ConfigPaths: server.URL, FilterRegex: ".+", ServerURL: "https://example.com/file.bin",
		Mode: SpeedModeFast, ProbeTimeout: time.Second, Concurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err == nil || !strings.Contains(err.Error(), "forbidden for remote sources") {
		t.Fatalf("direct remote config must retain remote origin, proxies=%#v err=%v", proxies, err)
	}
	if proxies != nil {
		t.Fatalf("remote file provider returned a partial proxy set: %#v", proxies)
	}
}

func TestPrepareConfigSourcesRejectsProviderSourceConflicts(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{
			name: "file with url",
			config: `type: file
    path: nodes.yaml
    url: https://provider.example/nodes`,
		},
		{
			name: "file with payload",
			config: `type: file
    path: nodes.yaml
    payload: []`,
		},
		{
			name: "http with cache path",
			config: `type: http
    url: https://provider.example/nodes
    path: cache.yaml`,
		},
		{
			name: "inline with url",
			config: `type: inline
    url: https://provider.example/nodes
    payload: []`,
		},
		{
			name:   "missing type",
			config: `url: https://provider.example/nodes`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "config.yaml")
			body := "proxy-providers:\n  conflicting:\n    " +
				strings.ReplaceAll(test.config, "\n", "\n    ") + "\nproxies: []\n"
			if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := PrepareConfigSources([]ConfigSource{{
				Path: configPath, Origin: SourceOriginLocal, LocalDependency: configPath,
			}}, directory, "")
			if err == nil {
				t.Fatalf("conflicting provider source unexpectedly succeeded: %#v", result)
			}
			if result != nil {
				t.Fatalf("conflicting provider source returned a partial result: %#v", result)
			}
		})
	}
}

func TestPrepareConfigSourcesLoadsLocalRelativeFileAndReportsDependencies(t *testing.T) {
	directory := t.TempDir()
	providerDirectory := filepath.Join(directory, "providers")
	if err := os.MkdirAll(providerDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	providerPath := filepath.Join(providerDirectory, "nodes.yaml")
	if err := os.WriteFile(providerPath, []byte(`payload:
  - name: local-node
    type: trojan
    server: local.example.com
    port: 443
    password: local-password
`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`proxy-providers:
  local:
    type: file
    path: providers/nodes.yaml
proxies: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	preparedDirectory := filepath.Join(directory, "prepared")
	result, err := PrepareConfigSources([]ConfigSource{{
		Path: configPath, Origin: SourceOriginLocal, LocalDependency: configPath,
	}}, preparedDirectory, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ConfigPaths) != 1 {
		t.Fatalf("prepared config paths=%#v, want one", result.ConfigPaths)
	}
	for _, dependency := range []string{configPath, providerPath} {
		if !containsPath(result.LocalDependencies, dependency) {
			t.Fatalf("local dependency %q missing from %#v", dependency, result.LocalDependencies)
		}
	}

	tester, err := New(&Config{
		ConfigPaths: result.ConfigPaths[0], FilterRegex: ".+", ServerURL: "https://example.com/file.bin",
		Mode: SpeedModeFast, ProbeTimeout: time.Second, Concurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatal(err)
	}
	if proxies["[local] local-node"] == nil {
		t.Fatalf("materialized local provider node is missing: %#v", proxies)
	}
}

func TestPrepareConfigSourcesSendsProviderHeadersSupportsURIAndFetchesOnce(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer provider-secret" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Cookie"); got != "provider_session=test-session" {
			http.Error(w, "missing cookie", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("X-Provider-Token"); got != "test-provider-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Values("X-Multi"); len(got) != 2 || got[0] != "first" || got[1] != "second" {
			http.Error(w, "missing multi-value header", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w,
			"trojan://provider-password@provider.example.com:443?sni=provider.example.com#uri-node")
	}))
	defer server.Close()

	directory := t.TempDir()
	configPath := filepath.Join(directory, "remote-config.yaml")
	configBody := fmt.Sprintf(`proxy-providers:
  protected:
    type: http
    url: %q
    header:
      Authorization:
        - Bearer provider-secret
      Cookie: provider_session=test-session
      X-Provider-Token: test-provider-token
      X-Multi:
        - first
        - second
proxies: []
`, server.URL)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := PrepareConfigSources([]ConfigSource{{
		Path: configPath, Origin: SourceOriginLocal, LocalDependency: configPath,
	}}, directory, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider requests=%d, want exactly one", got)
	}

	tester, err := New(&Config{
		ConfigPaths: result.ConfigPaths[0], FilterRegex: ".+", ServerURL: "https://example.com/file.bin",
		Mode: SpeedModeFast, ProbeTimeout: time.Second, Concurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		t.Fatal(err)
	}
	if proxies["[protected] uri-node"] == nil {
		t.Fatalf("URI-list provider node is missing: %#v", proxies)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("materialized provider was fetched again: requests=%d", got)
	}
}

func TestPrepareConfigSourcesRejectsMixedValidAndInvalidURIProviders(t *testing.T) {
	plainBody := []byte("trojan://provider-password@provider.example.com:443?sni=provider.example.com#valid-node\ninvalid-provider-line\n")
	tests := []struct {
		name string
		body []byte
	}{
		{name: "plaintext", body: plainBody},
		{name: "base64", body: []byte(base64.StdEncoding.EncodeToString(plainBody))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write(test.body)
			}))
			defer server.Close()

			directory := t.TempDir()
			oldOutputPath := filepath.Join(directory, "existing-output.yaml")
			const oldOutput = "old-output-must-survive\n"
			if err := os.WriteFile(oldOutputPath, []byte(oldOutput), 0o600); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(directory, "config.yaml")
			configBody := fmt.Sprintf(`proxy-providers:
  mixed-uri:
    type: http
    url: %q
proxies: []
`, server.URL)
			if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
				t.Fatal(err)
			}

			result, err := PrepareConfigSources([]ConfigSource{{
				Path: configPath, Origin: SourceOriginLocal, LocalDependency: configPath,
			}}, directory, "")
			if err == nil || !strings.Contains(err.Error(), "URI line 2") {
				t.Fatalf("mixed URI Provider must fail at the invalid line, result=%#v err=%v", result, err)
			}
			if result != nil {
				t.Fatalf("mixed URI Provider returned a partial result: %#v", result)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("mixed URI Provider requests=%d, want exactly one", got)
			}
			if _, statErr := os.Stat(filepath.Join(directory, "materialized-001.yaml")); !os.IsNotExist(statErr) {
				t.Fatalf("mixed URI Provider left materialized output: %v", statErr)
			}
			preservedOutput, readErr := os.ReadFile(oldOutputPath)
			if readErr != nil || string(preservedOutput) != oldOutput {
				t.Fatalf("mixed URI Provider changed old output: content=%q err=%v", preservedOutput, readErr)
			}
		})
	}
}

func TestPrepareConfigSourcesRejectsUnsupportedProviderFeaturesBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "proxies: []")
	}))
	defer server.Close()

	tests := []struct {
		name  string
		field string
	}{
		{name: "named fetch proxy", field: "proxy: upstream-proxy"},
		{name: "age encryption", field: "age-secret-key: AGE-SECRET-KEY-EXAMPLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "config.yaml")
			configBody := fmt.Sprintf(`proxy-providers:
  unsupported:
    type: http
    url: %q
    %s
proxies: []
`, server.URL, test.field)
			if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := PrepareConfigSources([]ConfigSource{{
				Path: configPath, Origin: SourceOriginRemote,
			}}, directory, "")
			if err == nil || !strings.Contains(err.Error(), "request was not sent") {
				t.Fatalf("unsupported feature must fail clearly before request, result=%#v err=%v", result, err)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("unsupported provider features issued %d requests", got)
	}
}

func TestPrepareConfigSourcesRejectsUnsafeProviderHeadersBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "proxies: []")
	}))
	defer server.Close()

	tests := []struct {
		name      string
		header    string
		wantError string
	}{
		{name: "Host", header: "Host: private.example", wantError: "Host is not supported"},
		{name: "line break in value", header: `X-Test: "safe\r\nInjected: true"`, wantError: "contains a line break"},
		{name: "line break in name", header: `"X-Test\r\nInjected": safe`, wantError: "invalid header name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "config.yaml")
			configBody := fmt.Sprintf(`proxy-providers:
  unsafe-header:
    type: http
    url: %q
    header:
      %s
proxies: []
`, server.URL, test.header)
			if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := PrepareConfigSources([]ConfigSource{{
				Path: configPath, Origin: SourceOriginLocal, LocalDependency: configPath,
			}}, directory, "")
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("unsafe Provider header did not fail in validation, result=%#v err=%v", result, err)
			}
			if result != nil {
				t.Fatalf("unsafe Provider header returned a partial result: %#v", result)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("unsafe Provider headers reached the server %d times", got)
	}
}

func TestPrepareConfigSourcesEnforcesProviderSizeLimitBoundary(t *testing.T) {
	providerBody := []byte("trojan://provider-password@provider.example.com:443?sni=provider.example.com#size-node")
	tests := []struct {
		name      string
		limit     int
		wantError bool
	}{
		{name: "exactly at limit", limit: len(providerBody)},
		{name: "one byte over limit", limit: len(providerBody) - 1, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write(providerBody)
			}))
			defer server.Close()

			directory := t.TempDir()
			configPath := filepath.Join(directory, "config.yaml")
			configBody := fmt.Sprintf(`proxy-providers:
  limited:
    type: http
    url: %q
    size-limit: %d
proxies: []
`, server.URL, test.limit)
			if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := PrepareConfigSources([]ConfigSource{{
				Path: configPath, Origin: SourceOriginLocal, LocalDependency: configPath,
			}}, directory, "")
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("size-limit %d", test.limit)) {
					t.Fatalf("Provider one byte over limit must fail clearly, result=%#v err=%v", result, err)
				}
				if result != nil {
					t.Fatalf("size-limited Provider returned a partial result: %#v", result)
				}
			} else if err != nil || result == nil || len(result.ConfigPaths) != 1 {
				t.Fatalf("Provider exactly at size-limit must succeed, result=%#v err=%v", result, err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("size-limited Provider requests=%d, want exactly one", got)
			}
		})
	}
}

func TestPrepareConfigSourcesRejectsMalformedProviderAndPreservesOldOutput(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "not: [valid")
	}))
	defer server.Close()

	directory := t.TempDir()
	oldOutputPath := filepath.Join(directory, "existing-output.yaml")
	const oldOutput = "old-output-must-survive\n"
	if err := os.WriteFile(oldOutputPath, []byte(oldOutput), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	configBody := fmt.Sprintf(`proxy-providers:
  malformed:
    type: http
    url: %q
proxies: []
`, server.URL)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := PrepareConfigSources([]ConfigSource{{
		Path: configPath, Origin: SourceOriginLocal, LocalDependency: configPath,
	}}, directory, "")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed Provider must fail the whole batch, result=%#v err=%v", result, err)
	}
	if result != nil {
		t.Fatalf("malformed Provider returned a partial result: %#v", result)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("malformed Provider requests=%d, want one", got)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "materialized-001.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("malformed Provider left materialized output: %v", statErr)
	}
	preservedOutput, readErr := os.ReadFile(oldOutputPath)
	if readErr != nil || string(preservedOutput) != oldOutput {
		t.Fatalf("malformed Provider changed old output: content=%q err=%v", preservedOutput, readErr)
	}
}

func TestPrepareConfigSourcesFailureCleansPartialMaterialization(t *testing.T) {
	directory := t.TempDir()
	oldOutputPath := filepath.Join(directory, "existing-output.yaml")
	const oldOutput = "old-output-must-survive\n"
	if err := os.WriteFile(oldOutputPath, []byte(oldOutput), 0o600); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(directory, "first.yaml")
	if err := os.WriteFile(firstPath, []byte(`proxy-providers:
  inline:
    type: inline
    payload:
      - name: valid-node
        type: trojan
        server: valid.example.com
        port: 443
        password: valid-password
proxies: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(directory, "second.yaml")
	if err := os.WriteFile(secondPath, []byte(`proxy-providers:
  broken:
    type: file
    path: missing-provider.yaml
proxies: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	preparedDirectory := filepath.Join(directory, "prepared")
	result, err := PrepareConfigSources([]ConfigSource{
		{Path: firstPath, Origin: SourceOriginLocal, LocalDependency: firstPath},
		{Path: secondPath, Origin: SourceOriginLocal, LocalDependency: secondPath},
	}, preparedDirectory, "")
	if err == nil {
		t.Fatalf("broken second provider unexpectedly succeeded: %#v", result)
	}
	if result != nil {
		t.Fatalf("broken batch returned partial preparation result: %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(preparedDirectory, "materialized-001.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("failed batch left a partial materialized config: %v", statErr)
	}
	preservedOutput, readErr := os.ReadFile(oldOutputPath)
	if readErr != nil || string(preservedOutput) != oldOutput {
		t.Fatalf("failed Provider batch changed old output: content=%q err=%v", preservedOutput, readErr)
	}
}

func containsPath(paths []string, expected string) bool {
	expected, _ = filepath.Abs(expected)
	for _, path := range paths {
		actual, _ := filepath.Abs(path)
		if strings.EqualFold(filepath.Clean(actual), filepath.Clean(expected)) {
			return true
		}
	}
	return false
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

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseClashYAML(t *testing.T) {
	body := []byte("proxies:\n  - name: test\n    type: vless\n    server: example.com\n    port: 443\n    uuid: 00000000-0000-0000-0000-000000000000\n")
	result, err := parseSubscription(body)
	if err != nil {
		t.Fatal(err)
	}
	if result.format != "clash-yaml" || result.proxies != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseBase64URIList(t *testing.T) {
	links := strings.Join([]string{
		"vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=tcp#VLESS",
		"hysteria2://password@example.org:443?sni=example.org#HY2",
	}, "\n")
	body := []byte(base64.StdEncoding.EncodeToString([]byte(links)))
	result, err := parseSubscription(body)
	if err != nil {
		t.Fatal(err)
	}
	if result.format != "base64-or-uri-list" || result.proxies != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseURIListFailsClosedWhenAnyLineIsInvalid(t *testing.T) {
	links := strings.Join([]string{
		"trojan://password@example.com:443?sni=example.com#valid",
		"unsupported://partial-secret@example.org:443#invalid",
	}, "\n")
	tests := []struct {
		name string
		body []byte
	}{
		{name: "plain", body: []byte(links)},
		{name: "base64", body: []byte(base64.StdEncoding.EncodeToString([]byte(links)))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := parseSubscription(test.body)
			if err == nil {
				t.Fatalf("mixed valid and invalid URI lines unexpectedly succeeded: %+v", result)
			}
			if result != nil {
				t.Fatalf("failed URI list returned a partial result: %+v", result)
			}
			if !strings.Contains(err.Error(), "line 2") {
				t.Fatalf("error does not identify the invalid line: %v", err)
			}
			if strings.Contains(err.Error(), "partial-secret") {
				t.Fatalf("error leaked URI credentials: %v", err)
			}
		})
	}
}

func TestLoadAndParsePreservesSanitizedInvalidURILineError(t *testing.T) {
	input := filepath.Join(t.TempDir(), "mixed-invalid.txt")
	links := strings.Join([]string{
		"trojan://password@example.com:443?sni=example.com#valid",
		"unsupported://partial-secret@example.org:443#invalid",
	}, "\n")
	if err := os.WriteFile(input, []byte(links), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := loadAndParse(input, defaultUserAgent, time.Second)
	if err == nil {
		t.Fatalf("mixed valid and invalid URI file unexpectedly succeeded: %+v", result)
	}
	if result != nil {
		t.Fatalf("failed URI file returned a partial result: %+v", result)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("CLI parse error does not preserve the invalid line: %v", err)
	}
	if strings.Contains(err.Error(), "partial-secret") {
		t.Fatalf("CLI parse error leaked URI credentials: %v", err)
	}
}

func TestParseSupportedURIFormats(t *testing.T) {
	vmessJSON, err := json.Marshal(map[string]any{
		"v": "2", "ps": "vmess", "add": "example.com", "port": "443",
		"id": "11111111-1111-1111-1111-111111111111", "aid": "0",
		"net": "tcp", "type": "none", "host": "", "path": "", "tls": "",
	})
	if err != nil {
		t.Fatal(err)
	}

	ssCredential := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:password"))
	ssrPassword := base64.RawURLEncoding.EncodeToString([]byte("password"))
	ssrRemark := base64.RawURLEncoding.EncodeToString([]byte("ssr"))
	ssrBody := "example.com:443:auth_sha1_v4:aes-256-cfb:tls1.2_ticket_auth:" +
		ssrPassword + "/?remarks=" + ssrRemark
	proxyCredential := base64.StdEncoding.EncodeToString([]byte("user:password"))

	tests := []struct {
		name      string
		uri       string
		proxyType string
	}{
		{"vless", "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=tcp#vless", "vless"},
		{"vmess", "vmess://" + base64.StdEncoding.EncodeToString(vmessJSON), "vmess"},
		{"trojan", "trojan://password@example.com:443?security=tls#trojan", "trojan"},
		{"shadowsocks", "ss://" + ssCredential + "@example.com:443#ss", "ss"},
		{"shadowsocksr", "ssr://" + base64.RawURLEncoding.EncodeToString([]byte(ssrBody)), "ssr"},
		{"hysteria", "hysteria://example.com:443?auth=password&peer=example.com&upmbps=10&downmbps=20#hysteria", "hysteria"},
		{"hysteria2", "hysteria2://password@example.com:443?sni=example.com#hysteria2", "hysteria2"},
		{"tuic", "tuic://22222222-2222-2222-2222-222222222222:password@example.com:443?sni=example.com#tuic", "tuic"},
		{"anytls", "anytls://password@example.com:443?sni=example.com#anytls", "anytls"},
		{"http", "http://" + proxyCredential + "@example.com:8080#http", "http"},
		{"socks5", "socks5://" + proxyCredential + "@example.com:1080#socks5", "socks5"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := parseSubscription([]byte(test.uri))
			if err != nil {
				t.Fatal(err)
			}
			if result.format != "base64-or-uri-list" || result.proxies != 1 {
				t.Fatalf("unexpected result: %+v", result)
			}

			var config rawConfig
			if err := yaml.Unmarshal(result.body, &config); err != nil {
				t.Fatal(err)
			}
			if got := config.Proxies[0]["type"]; got != test.proxyType {
				t.Fatalf("expected proxy type %q, got %#v", test.proxyType, got)
			}
		})
	}
}

func TestLoadDirectNodeURI(t *testing.T) {
	input := "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=tcp#VLESS"
	result, err := loadAndParse(input, "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.format != "base64-or-uri-list" || result.proxies != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestLoadLocalConfigNormalizesRelativeFileProviderPath(t *testing.T) {
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(inputPath, []byte(`proxy-providers:
  local:
    type: file
    path: providers/nodes.yaml
proxies: []
`), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := loadAndParse(inputPath, "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var config rawConfig
	if err := yaml.Unmarshal(result.body, &config); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(directory, "providers", "nodes.yaml")
	got, _ := config.Providers["local"]["path"].(string)
	if got != want {
		t.Fatalf("normalized provider path=%q, want %q", got, want)
	}
}

func TestReadInputRetriesTransientEOF(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&attempts, 1)
		if current < maxHTTPAttempts {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("test server does not support hijacking")
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = connection.Close()
			return
		}
		if r.UserAgent() != defaultUserAgent {
			t.Errorf("unexpected User-Agent: %s", r.UserAgent())
		}
		_, _ = w.Write([]byte("proxies:\n  - name: recovered\n"))
	}))
	defer server.Close()

	body, err := readInput(server.URL, defaultUserAgent, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&attempts) != maxHTTPAttempts {
		t.Fatalf("expected %d attempts, got %d", maxHTTPAttempts, attempts)
	}
	if !strings.Contains(string(body), "recovered") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestReadHTTPInputAcceptsSizeLimitAndRejectsOneByteMore(t *testing.T) {
	var oversizedAttempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := int64(maxHTTPInputBytes)
		if r.URL.Path == "/oversized" {
			atomic.AddInt32(&oversizedAttempts, 1)
			size++
		}
		_, _ = io.CopyN(w, repeatedByteReader('x'), size)
	}))
	defer server.Close()

	body, err := readHTTPInput(server.URL+"/boundary", defaultUserAgent, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != maxHTTPInputBytes {
		t.Fatalf("expected %d bytes, got %d", maxHTTPInputBytes, len(body))
	}

	body, err = readHTTPInput(server.URL+"/oversized", defaultUserAgent, 5*time.Second)
	if !errors.Is(err, errHTTPInputTooLarge) {
		t.Fatalf("expected size-limit error, got %v", err)
	}
	if body != nil {
		t.Fatalf("oversized response returned %d bytes", len(body))
	}
	if got := atomic.LoadInt32(&oversizedAttempts); got != 1 {
		t.Fatalf("oversized response must not be retried, got %d attempts", got)
	}
}

func TestReadHTTPInputRetriesTransientStatuses(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := atomic.AddInt32(&attempts, 1)
				if current < maxHTTPAttempts {
					http.Error(w, "temporary failure", status)
					return
				}
				_, _ = w.Write([]byte("recovered"))
			}))
			defer server.Close()

			body, err := readHTTPInput(server.URL, defaultUserAgent, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "recovered" {
				t.Fatalf("unexpected body: %q", body)
			}
			if got := atomic.LoadInt32(&attempts); got != maxHTTPAttempts {
				t.Fatalf("expected %d attempts, got %d", maxHTTPAttempts, got)
			}
		})
	}
}

func TestReadHTTPInputDoesNotRetryPermanentStatus(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "subscription-body-secret", http.StatusUnauthorized)
	}))
	defer server.Close()

	input := server.URL + "/subscription?token=url-token-secret"
	_, err := readHTTPInput(input, defaultUserAgent, time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("expected HTTP 401 error, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("permanent status must not be retried, got %d attempts", got)
	}
	assertErrorRedacted(t, err, input, "url-token-secret", "subscription-body-secret")
}

func TestReadHTTPInputTimeoutIsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	input := server.URL + "/subscription?token=timeout-token-secret"
	_, err := readHTTPInput(input, defaultUserAgent, 50*time.Millisecond)
	if !errors.Is(err, errHTTPTimeout) {
		t.Fatalf("expected timeout error, got %v", err)
	}
	assertErrorRedacted(t, err, input, "timeout-token-secret")
}

func TestReadHTTPInputNetworkErrorIsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	input := server.URL + "/subscription?token=network-token-secret"
	server.Close()

	_, err := readHTTPInput(input, defaultUserAgent, 2*time.Second)
	if !errors.Is(err, errHTTPNetwork) {
		t.Fatalf("expected network error, got %v", err)
	}
	assertErrorRedacted(t, err, input, "network-token-secret")
}

func TestReadHTTPInputInvalidURLIsRedacted(t *testing.T) {
	input := "http://[::1/subscription?token=invalid-token-secret"
	_, err := readHTTPInput(input, defaultUserAgent, time.Second)
	if !errors.Is(err, errInvalidSubscriptionURL) {
		t.Fatalf("expected invalid URL error, got %v", err)
	}
	assertErrorRedacted(t, err, input, "invalid-token-secret")
}

func TestParseThousandNodeLines(t *testing.T) {
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=tcp#node"
	}
	result, err := parseSubscription([]byte(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if result.proxies != 1000 {
		t.Fatalf("expected 1000 proxies, got %d", result.proxies)
	}
}

func TestInlineNodeDetection(t *testing.T) {
	if !isInlineNode("Trojan://password@example.com:443#node") {
		t.Fatal("expected Trojan URI to be treated as an inline node")
	}
	if isInlineNode("https://example.com/subscription") {
		t.Fatal("HTTP subscription URL must remain a remote input")
	}
}

func TestWithoutMetaFlag(t *testing.T) {
	input := "https://example.com/sub?token=abc&flag=meta"
	got := withoutMetaFlag(input)
	if got != "https://example.com/sub?token=abc" {
		t.Fatalf("unexpected URL: %s", got)
	}
}

type repeatedByteReader byte

func (r repeatedByteReader) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = byte(r)
	}
	return len(buffer), nil
}

func assertErrorRedacted(t *testing.T, err error, secrets ...string) {
	t.Helper()
	message := err.Error()
	for _, secret := range secrets {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked sensitive input %q: %s", secret, message)
		}
	}
}

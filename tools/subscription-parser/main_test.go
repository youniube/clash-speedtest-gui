package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

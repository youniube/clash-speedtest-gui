package shareurl

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/common/convert"
)

func TestGenerateVLESSPreservesTransportAndReality(t *testing.T) {
	link, err := Generate(map[string]any{
		"name":               "香港 01",
		"type":               "vless",
		"server":             "edge.example.com",
		"port":               443,
		"uuid":               "00000000-0000-0000-0000-000000000000",
		"network":            "ws",
		"flow":               "xtls-rprx-vision",
		"servername":         "origin.example.com",
		"client-fingerprint": "chrome",
		"alpn":               []string{"h2", "http/1.1"},
		"reality-opts": map[string]any{
			"public-key": "public-key",
			"short-id":   "abcd",
		},
		"ws-opts": map[string]any{
			"path": "/websocket",
			"headers": map[string]any{
				"Host": "cdn.example.com",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	assertQuery(t, query, "security", "reality")
	assertQuery(t, query, "pbk", "public-key")
	assertQuery(t, query, "sid", "abcd")
	assertQuery(t, query, "flow", "xtls-rprx-vision")
	assertQuery(t, query, "fp", "chrome")
	assertQuery(t, query, "alpn", "h2,http/1.1")
	assertQuery(t, query, "host", "cdn.example.com")
	assertQuery(t, query, "path", "/websocket")
}

func TestGenerateTrojanPreservesGRPC(t *testing.T) {
	link, err := Generate(map[string]any{
		"name":        "trojan-node",
		"type":        "trojan",
		"server":      "trojan.example.com",
		"port":        "443",
		"password":    "secret",
		"network":     "grpc",
		"sni":         "sni.example.com",
		"grpc-opts":   map[string]any{"grpc-service-name": "service-name"},
		"alpn":        []any{"h2"},
		"fingerprint": "certificate-pin",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	assertQuery(t, parsed.Query(), "type", "grpc")
	assertQuery(t, parsed.Query(), "serviceName", "service-name")
	assertQuery(t, parsed.Query(), "sni", "sni.example.com")
	assertQuery(t, parsed.Query(), "pcs", "certificate-pin")
}

func TestGenerateVMessUsesV2RayNJSON(t *testing.T) {
	link, err := Generate(map[string]any{
		"name":       "vmess-node",
		"type":       "vmess",
		"server":     "vmess.example.com",
		"port":       8443,
		"uuid":       "11111111-1111-1111-1111-111111111111",
		"alterId":    0,
		"cipher":     "auto",
		"network":    "ws",
		"tls":        true,
		"servername": "sni.example.com",
		"ws-opts": map[string]any{
			"path":    "/vmess",
			"headers": map[string]any{"Host": "cdn.example.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	if values["net"] != "ws" || values["host"] != "cdn.example.com" || values["path"] != "/vmess" {
		t.Fatalf("vmess transport fields missing: %#v", values)
	}
	if values["tls"] != "tls" || values["sni"] != "sni.example.com" {
		t.Fatalf("vmess TLS fields missing: %#v", values)
	}
}

func TestGenerateRemainingSupportedProtocols(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		prefix string
		parts  []string
	}{
		{
			name: "ss",
			config: map[string]any{
				"name": "ss-node", "type": "ss", "server": "ss.example.com", "port": 443,
				"cipher": "aes-128-gcm", "password": "secret", "plugin": "v2ray-plugin",
				"plugin-opts": map[string]any{"mode": "websocket", "host": "cdn.example.com", "path": "/ss", "tls": true},
			},
			prefix: "ss://",
			parts:  []string{"plugin=v2ray-plugin", "cdn.example.com"},
		},
		{
			name: "ssr",
			config: map[string]any{
				"name": "ssr-node", "type": "ssr", "server": "ssr.example.com", "port": 443,
				"protocol": "auth_sha1_v4", "cipher": "aes-256-cfb", "obfs": "tls1.2_ticket_auth",
				"password": "secret", "obfs-param": "cdn.example.com", "protocol-param": "param",
			},
			prefix: "ssr://",
			parts:  nil,
		},
		{
			name: "hysteria2",
			config: map[string]any{
				"name": "hy2-node", "type": "hysteria2", "server": "hy2.example.com", "port": 443,
				"password": "secret", "sni": "sni.example.com", "obfs": "salamander", "obfs-password": "obfs-secret",
			},
			prefix: "hysteria2://",
			parts:  []string{"obfs=salamander", "obfs-password=obfs-secret"},
		},
		{
			name: "tuic",
			config: map[string]any{
				"name": "tuic-node", "type": "tuic", "server": "tuic.example.com", "port": 443,
				"uuid": "22222222-2222-2222-2222-222222222222", "password": "secret", "sni": "sni.example.com",
				"congestion-controller": "bbr", "udp-relay-mode": "native",
			},
			prefix: "tuic://",
			parts:  []string{"congestion_control=bbr", "udp_relay_mode=native"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link, err := Generate(test.config)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(link, test.prefix) {
				t.Fatalf("unexpected link: %s", link)
			}
			for _, part := range test.parts {
				if !strings.Contains(link, part) {
					t.Fatalf("link %q does not contain %q", link, part)
				}
			}
		})
	}
}

func TestGenerateAnyTLSPreservesSupportedFields(t *testing.T) {
	link, err := Generate(map[string]any{
		"name":             "AnyTLS 台湾 01",
		"type":             "anytls",
		"server":           "2001:db8::1",
		"port":             443,
		"password":         "p@ss:/?#word",
		"sni":              "origin.example.com",
		"fingerprint":      "sha256-pin",
		"skip-cert-verify": true,
		"udp":              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "anytls" || parsed.Hostname() != "2001:db8::1" || parsed.Port() != "443" {
		t.Fatalf("unexpected AnyTLS endpoint: %s", link)
	}
	if parsed.User.Username() != "p@ss:/?#word" {
		t.Fatalf("AnyTLS password was not escaped safely: %s", link)
	}
	assertQuery(t, parsed.Query(), "sni", "origin.example.com")
	assertQuery(t, parsed.Query(), "hpkp", "sha256-pin")
	assertQuery(t, parsed.Query(), "insecure", "1")
	if parsed.Fragment != "AnyTLS 台湾 01" {
		t.Fatalf("unexpected AnyTLS name: %q", parsed.Fragment)
	}
}

func TestGenerateAnyTLSRoundTripsThroughProductionConverter(t *testing.T) {
	source := map[string]any{
		"name":             "AnyTLS round trip",
		"type":             "anytls",
		"server":           "edge.example.com",
		"port":             8443,
		"password":         "secret:@/value",
		"sni":              "sni.example.com",
		"fingerprint":      "certificate-pin",
		"skip-cert-verify": true,
		"udp":              true,
	}
	link, err := Generate(source)
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 {
		t.Fatalf("expected one round-tripped proxy, got %d", len(proxies))
	}
	actual := proxies[0]
	for _, key := range []string{"name", "type", "server", "password", "sni", "fingerprint"} {
		if scalarString(actual[key]) != scalarString(source[key]) {
			t.Fatalf("round-trip field %s: expected %#v, got %#v", key, source[key], actual[key])
		}
	}
	if scalarString(actual["port"]) != scalarString(source["port"]) {
		t.Fatalf("round-trip port: expected %#v, got %#v", source["port"], actual["port"])
	}
	if !boolValue(actual, "skip-cert-verify") || !boolValue(actual, "udp") {
		t.Fatalf("round-trip booleans were not preserved: %#v", actual)
	}
}

func TestGenerateAnyTLSRejectsLossyConfigurations(t *testing.T) {
	base := map[string]any{
		"name": "anytls", "type": "anytls", "server": "example.com", "port": 443,
		"password": "secret", "udp": true,
	}
	tests := []struct {
		name  string
		field string
		value any
	}{
		{name: "ALPN", field: "alpn", value: []any{"h2"}},
		{name: "client fingerprint", field: "client-fingerprint", value: "chrome"},
		{name: "ECH", field: "ech-opts", value: map[string]any{"enable": true}},
		{name: "client certificate", field: "certificate", value: "certificate"},
		{name: "session tuning", field: "idle-session-timeout", value: 30},
		{name: "disabled UDP", field: "udp", value: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := make(map[string]any, len(base)+1)
			for key, value := range base {
				config[key] = value
			}
			config[test.field] = test.value
			if _, err := Generate(config); err == nil {
				t.Fatalf("AnyTLS configuration with %s must not be copied lossily", test.field)
			}
		})
	}
}

func TestGenerateRejectsUnsupportedProtocol(t *testing.T) {
	if _, err := Generate(map[string]any{"type": "wireguard"}); err == nil {
		t.Fatal("unsupported protocol must return an error")
	}
}

func assertQuery(t *testing.T, values url.Values, key, expected string) {
	t.Helper()
	if actual := values.Get(key); actual != expected {
		t.Fatalf("query %s: expected %q, got %q", key, expected, actual)
	}
}

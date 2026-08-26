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

func TestGenerateRejectsLossyConfigurationsForEveryProtocol(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		field  string
		value  any
	}{
		{
			name: "vless routing dependency",
			config: map[string]any{
				"name": "vless", "type": "vless", "server": "example.com", "port": 443,
				"uuid": "00000000-0000-0000-0000-000000000000",
			},
			field: "dialer-proxy", value: "upstream",
		},
		{
			name: "vless insecure TLS unsupported by production importer",
			config: map[string]any{
				"name": "vless", "type": "vless", "server": "example.com", "port": 443,
				"uuid": "00000000-0000-0000-0000-000000000000", "tls": true,
			},
			field: "skip-cert-verify", value: true,
		},
		{
			name: "vless advanced reality option",
			config: map[string]any{
				"name": "vless", "type": "vless", "server": "example.com", "port": 443,
				"uuid":         "00000000-0000-0000-0000-000000000000",
				"reality-opts": map[string]any{"public-key": "key", "short-id": "id", "spider-x": "/"},
			},
			field: "unused", value: false,
		},
		{
			name: "trojan flow",
			config: map[string]any{
				"name": "trojan", "type": "trojan", "server": "example.com", "port": 443,
				"password": "secret",
			},
			field: "flow", value: "xtls-rprx-vision",
		},
		{
			name: "vmess insecure TLS",
			config: map[string]any{
				"name": "vmess", "type": "vmess", "server": "example.com", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111111",
			},
			field: "skip-cert-verify", value: true,
		},
		{
			name: "vmess client fingerprint unsupported by production importer",
			config: map[string]any{
				"name": "vmess", "type": "vmess", "server": "example.com", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111111", "tls": true,
			},
			field: "client-fingerprint", value: "chrome",
		},
		{
			name: "ss unsupported plugin option",
			config: map[string]any{
				"name": "ss", "type": "ss", "server": "example.com", "port": 443,
				"cipher": "aes-128-gcm", "password": "secret", "plugin": "v2ray-plugin",
			},
			field: "plugin-opts", value: map[string]any{"mux": true},
		},
		{
			name: "ssr interface binding",
			config: map[string]any{
				"name": "ssr", "type": "ssr", "server": "example.com", "port": 443,
				"protocol": "auth_sha1_v4", "cipher": "aes-256-cfb", "obfs": "plain", "password": "secret",
			},
			field: "interface-name", value: "Ethernet",
		},
		{
			name: "hysteria2 port hopping",
			config: map[string]any{
				"name": "hy2", "type": "hysteria2", "server": "example.com", "port": 443,
				"password": "secret",
			},
			field: "ports", value: "20000-30000",
		},
		{
			name: "tuic reduce RTT",
			config: map[string]any{
				"name": "tuic", "type": "tuic", "server": "example.com", "port": 443,
				"uuid": "22222222-2222-2222-2222-222222222222", "password": "secret",
			},
			field: "reduce-rtt", value: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := make(map[string]any, len(test.config)+1)
			for key, value := range test.config {
				config[key] = value
			}
			config[test.field] = test.value
			if _, err := Generate(config); err == nil {
				t.Fatalf("configuration must not be copied lossily: %#v", config)
			}
		})
	}
}

func TestGenerateRejectsLossyNestedTransportFields(t *testing.T) {
	tests := []struct {
		name    string
		network string
		options map[string]any
	}{
		{name: "extra WebSocket header", network: "ws", options: map[string]any{
			"path": "/ws", "headers": map[string]any{"Host": "cdn.example.com", "User-Agent": "custom"},
		}},
		{name: "gRPC authority", network: "grpc", options: map[string]any{
			"grpc-service-name": "service", "grpc-authority": "authority.example.com",
		}},
		{name: "advanced XHTTP mode", network: "xhttp", options: map[string]any{
			"path": "/xhttp", "mode": "packet-up",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field := map[string]string{"ws": "ws-opts", "grpc": "grpc-opts", "xhttp": "xhttp-opts"}[test.network]
			config := map[string]any{
				"name": "node", "type": "vless", "server": "example.com", "port": 443,
				"uuid": "00000000-0000-0000-0000-000000000000", "network": test.network,
				field: test.options,
			}
			if _, err := Generate(config); err == nil {
				t.Fatalf("nested transport options must not be copied lossily: %#v", config)
			}
		})
	}
}

func TestGenerateAcceptsLowercaseHostHeader(t *testing.T) {
	link, err := Generate(map[string]any{
		"name": "node", "type": "vless", "server": "example.com", "port": 443,
		"uuid": "00000000-0000-0000-0000-000000000000", "network": "ws",
		"ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"host": "cdn.example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	assertQuery(t, parsed.Query(), "host", "cdn.example.com")
}

func TestGenerateSupportedProtocolsRoundTripAllConnectionSemantics(t *testing.T) {
	tests := []struct {
		name   string
		source map[string]any
	}{
		{
			name: "VLESS Reality WebSocket",
			source: map[string]any{
				"name": "duplicate", "type": "vless", "server": "[2001:db8::10]", "port": "0443",
				"uuid": "00000000-0000-0000-0000-000000000000", "encryption": "none",
				"flow": "xtls-rprx-vision", "network": "ws", "sni": "Origin.Example.COM",
				"client-fingerprint": "chrome", "fingerprint": "certificate-pin",
				"alpn": []any{"h2", "http/1.1"}, "udp": "1",
				"reality-opts": map[string]any{"public-key": "public-key", "short-id": "abcd"},
				"ws-opts": map[string]any{
					"path": "/socket", "headers": map[string]any{"Host": "CDN.Example.COM"},
					"max-early-data": "2048", "early-data-header-name": "Sec-WebSocket-Protocol",
				},
			},
		},
		{
			name: "Trojan gRPC",
			source: map[string]any{
				"name": "duplicate", "type": "trojan", "server": "trojan.example.com", "port": 443,
				"password": "test-credential", "network": "grpc", "sni": "SNI.Example.COM",
				"client-fingerprint": "firefox", "fingerprint": "certificate-pin",
				"alpn": []string{"h2"}, "skip-cert-verify": "true", "udp": true,
				"grpc-opts": map[string]any{"grpc-service-name": "service/name"},
			},
		},
		{
			name: "VMess TLS WebSocket",
			source: map[string]any{
				"name": "duplicate", "type": "vmess", "server": "vmess.example.com", "port": "443",
				"uuid": "11111111-1111-1111-1111-111111111111", "alterId": "0", "cipher": "auto",
				"network": "ws", "tls": "1", "sni": "origin.example.com", "alpn": []any{"h2"},
				"udp": true, "ws-opts": map[string]any{
					"path": "/vmess", "headers": map[string]any{"host": "cdn.example.com"},
				},
			},
		},
		{
			name: "Shadowsocks v2ray plugin",
			source: map[string]any{
				"name": "duplicate", "type": "ss", "server": "ss.example.com", "port": 443,
				"cipher": "aes-128-gcm", "password": "test-credential", "plugin": "v2ray-plugin",
				"udp-over-tcp": "1", "plugin-opts": map[string]any{
					"mode": "websocket", "host": "CDN.Example.COM", "path": "/plugin", "tls": true,
				},
			},
		},
		{
			name: "ShadowsocksR parameters",
			source: map[string]any{
				"name": "duplicate", "type": "ssr", "server": "ssr.example.com", "port": 443,
				"protocol": "auth_sha1_v4", "cipher": "aes-256-cfb", "obfs": "tls1.2_ticket_auth",
				"password": "test-credential", "obfs-param": "cdn.example.com",
				"protocol-param": "user:token",
			},
		},
		{
			name: "Hysteria2 TLS options",
			source: map[string]any{
				"name": "duplicate", "type": "hy2", "server": "hy2.example.com", "port": 443,
				"password": "test-credential", "sni": "SNI.Example.COM", "obfs": "salamander",
				"obfs-password": "obfs-credential", "fingerprint": "certificate-pin",
				"alpn": []string{"h3"}, "up": "20 Mbps", "down": "100 Mbps",
				"skip-cert-verify": true,
			},
		},
		{
			name: "TUIC authentication and transport",
			source: map[string]any{
				"name": "duplicate", "type": "tuic", "server": "tuic.example.com", "port": 443,
				"uuid":     "22222222-2222-2222-2222-222222222222",
				"password": "test-credential", "sni": "SNI.Example.COM", "alpn": []string{"h3"},
				"congestion-controller": "bbr", "udp-relay-mode": "native", "disable-sni": true,
			},
		},
		{
			name: "TUIC token authentication",
			source: map[string]any{
				"name": "duplicate", "type": "tuic", "server": "tuic-token.example.com", "port": 443,
				"token": "test-token", "sni": "token-sni.example.com", "udp": "true",
			},
		},
		{
			name: "AnyTLS IPv6",
			source: map[string]any{
				"name": "duplicate", "type": "anytls", "server": "2001:db8::20", "port": 443,
				"password": "test-credential", "sni": "SNI.Example.COM",
				"fingerprint": "certificate-pin", "skip-cert-verify": true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link, err := Generate(test.source)
			if err != nil {
				t.Fatal(err)
			}
			assertFullProductionRoundTrip(t, test.source, link)
		})
	}
}

func TestGenerateRejectsTrojanWebSocketFieldsDroppedByProductionImporter(t *testing.T) {
	tests := []struct {
		name  string
		opts  map[string]any
		field string
	}{
		{
			name: "Host",
			opts: map[string]any{
				"path": "/socket", "headers": map[string]any{"Host": "cdn.example.com"},
			},
			field: "ws-opts.headers.Host",
		},
		{
			name: "early data",
			opts: map[string]any{
				"path": "/socket", "max-early-data": 2048,
				"early-data-header-name": "Sec-WebSocket-Protocol",
			},
			field: "ws-opts.early-data-header-name",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := map[string]any{
				"name": "trojan-ws", "type": "trojan", "server": "trojan.example.com", "port": 443,
				"password": "sensitive-password", "network": "ws", "ws-opts": test.opts,
			}
			_, err := Generate(source)
			if err == nil {
				t.Fatal("lossy Trojan WebSocket URL must be rejected")
			}
			if !strings.Contains(err.Error(), test.field) {
				t.Fatalf("error must identify the changed field, got %q", err)
			}
			if strings.Contains(err.Error(), "sensitive-password") {
				t.Fatal("error leaked the Trojan password")
			}
		})
	}
}

func TestGenerateShadowsocksObfsRoundTrip(t *testing.T) {
	source := map[string]any{
		"name": "ss-obfs", "type": "ss", "server": "ss.example.com", "port": 443,
		"cipher": "aes-128-gcm", "password": "test-credential", "plugin": "obfs",
		"plugin-opts": map[string]any{"mode": "http", "host": "CDN.Example.COM"},
	}
	link, err := Generate(source)
	if err != nil {
		t.Fatal(err)
	}
	assertFullProductionRoundTrip(t, source, link)
	proxies, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil {
		t.Fatal(err)
	}
	actual := proxies[0]
	if got := stringValue(actual, "plugin"); got != "obfs" {
		t.Fatalf("production import changed Shadowsocks plugin: got %q", got)
	}
	opts := mapValue(actual, "plugin-opts")
	if got := stringValue(opts, "mode"); got != "http" {
		t.Fatalf("production import changed Shadowsocks plugin mode: got %q", got)
	}
	if got := stringValue(opts, "host"); got != "CDN.Example.COM" {
		t.Fatalf("production import changed Shadowsocks plugin host: got %q", got)
	}
}

func TestGenerateRejectsSSRIPv6UnsupportedByProductionImporter(t *testing.T) {
	source := map[string]any{
		"name": "ssr-ipv6", "type": "ssr", "server": "2001:db8::1234", "port": 443,
		"protocol": "auth_sha1_v4", "cipher": "aes-256-cfb", "obfs": "plain",
		"password": "sensitive-password",
	}
	_, err := Generate(source)
	if err == nil {
		t.Fatal("SSR IPv6 must be rejected while the production importer cannot parse it")
	}
	if !strings.Contains(err.Error(), "not accepted by the production importer") {
		t.Fatalf("unexpected rejection: %q", err)
	}
	for _, sensitive := range []string{"2001:db8::1234", "sensitive-password", "ssr://"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error leaked sensitive input %q", sensitive)
		}
	}
}

func TestGenerateRejectsSSRUDPOverTCPDroppedByProductionImporter(t *testing.T) {
	source := map[string]any{
		"name": "ssr-uot", "type": "ssr", "server": "ssr.example.com", "port": 443,
		"protocol": "auth_sha1_v4", "cipher": "aes-256-cfb", "obfs": "plain",
		"password": "sensitive-password", "udp-over-tcp": true,
	}
	_, err := Generate(source)
	if err == nil || !strings.Contains(err.Error(), "udp-over-tcp") {
		t.Fatalf("production importer field loss must be rejected, got %v", err)
	}
	if strings.Contains(err.Error(), "sensitive-password") {
		t.Fatal("error leaked the SSR password")
	}
}

func TestValidateGeneratedURLRejectsSkippedZeroAndMultipleNodes(t *testing.T) {
	source := map[string]any{
		"name": "node", "type": "ss", "server": "ss.example.com", "port": 443,
		"cipher": "aes-128-gcm", "password": "test-credential",
	}
	raw, err := generateRaw(source)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("unknown or damaged URL", func(t *testing.T) {
		for _, link := range []string{
			"unknown://damaged-node", "ss://not-valid", "unknown://skipped-node\n" + raw,
		} {
			if err := validateGeneratedURL(source, link); err == nil {
				t.Fatalf("production importer skip/error must reject %q", strings.SplitN(link, ":", 2)[0])
			}
		}
	})

	t.Run("zero nodes", func(t *testing.T) {
		err := validateImportedProxies("ss", source, nil)
		if err == nil || !strings.Contains(err.Error(), "returned 0 nodes") {
			t.Fatalf("expected exact zero-node error, got %v", err)
		}
	})

	t.Run("multiple nodes", func(t *testing.T) {
		proxies, importErr := convert.ConvertsV2Ray([]byte(raw + "\n" + raw))
		if importErr != nil {
			t.Fatal(importErr)
		}
		err := validateImportedProxies("ss", source, proxies)
		if err == nil || !strings.Contains(err.Error(), "returned 2 nodes") {
			t.Fatalf("expected exact multiple-node error, got %v", err)
		}
	})
}

func TestGenerateErrorsDoNotLeakCredentialsOrSerializedInputs(t *testing.T) {
	source := map[string]any{
		"name": "private-node", "type": "trojan", "server": "private.example.com", "port": 443,
		"password": "password-do-not-log", "network": "ws",
		"ws-opts": map[string]any{"headers": map[string]any{"Host": "private-cdn.example.com"}},
	}
	raw, err := generateRaw(source)
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Generate(source)
	if err == nil {
		t.Fatal("lossy source must fail")
	}
	for _, sensitive := range []string{
		"password-do-not-log", "00000000-0000-0000-0000-000000000000", "token-do-not-log",
		raw, string(serialized), "private-cdn.example.com",
	} {
		if sensitive != "" && strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error leaked sensitive content")
		}
	}

	for _, extra := range []map[string]any{
		{
			"name": "uuid-node", "type": "vless", "server": "example.com", "port": 443,
			"uuid": "00000000-0000-0000-0000-000000000000", "unsupported-auth-option": true,
		},
		{
			"name": "token-node", "type": "tuic", "server": "example.com", "port": 443,
			"token": "token-do-not-log", "reduce-rtt": true,
		},
	} {
		_, extraErr := Generate(extra)
		if extraErr == nil {
			t.Fatal("unsupported connection field must fail")
		}
		for _, sensitive := range []string{
			"00000000-0000-0000-0000-000000000000", "token-do-not-log",
		} {
			if strings.Contains(extraErr.Error(), sensitive) {
				t.Fatal("error leaked UUID or token")
			}
		}
	}
}

func assertFullProductionRoundTrip(t *testing.T, source map[string]any, link string) {
	t.Helper()
	proxies, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 {
		t.Fatalf("expected one round-tripped proxy, got %d", len(proxies))
	}
	expected, err := connectionSemantics(source)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := connectionSemantics(proxies[0])
	if err != nil {
		t.Fatal(err)
	}
	if field := firstSemanticDifference(expected, actual); field != "" {
		t.Fatalf("production round-trip changed connection field %s", field)
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

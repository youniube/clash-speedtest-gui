package shareurl

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Generate builds a share URL directly from a Mihomo proxy configuration.
// It never reparses a URL, so nested transport and TLS options remain available
// while the protocol-specific URL is being assembled.
func Generate(config map[string]any) (string, error) {
	proxyType := strings.ToLower(stringValue(config, "type"))
	switch proxyType {
	case "vless":
		return generateV(config, "vless", "uuid")
	case "trojan":
		return generateV(config, "trojan", "password")
	case "vmess":
		return generateVMess(config)
	case "ss":
		return generateSS(config)
	case "ssr":
		return generateSSR(config)
	case "hysteria2", "hy2":
		return generateHysteria2(config)
	case "tuic":
		return generateTUIC(config)
	case "anytls":
		return generateAnyTLS(config)
	default:
		return "", fmt.Errorf("unsupported share URL protocol: %s", proxyType)
	}
}

func generateV(config map[string]any, scheme, credentialKey string) (string, error) {
	server, port, err := endpoint(config)
	if err != nil {
		return "", err
	}
	credential := stringValue(config, credentialKey)
	if credential == "" {
		return "", fmt.Errorf("%s credential is empty", scheme)
	}

	query := url.Values{}
	network := strings.ToLower(stringValue(config, "network"))
	if network == "" {
		network = "tcp"
	}
	query.Set("type", network)
	if scheme == "vless" {
		encryption := stringValue(config, "encryption")
		if encryption == "" {
			encryption = "none"
		}
		query.Set("encryption", encryption)
		setIfPresent(query, "flow", stringValue(config, "flow"))
	}

	reality := mapValue(config, "reality-opts")
	if len(reality) > 0 {
		query.Set("security", "reality")
		setIfPresent(query, "pbk", stringValue(reality, "public-key"))
		setIfPresent(query, "sid", stringValue(reality, "short-id"))
	} else if boolValue(config, "tls") || scheme == "trojan" {
		query.Set("security", "tls")
	} else {
		query.Set("security", "none")
	}

	sni := firstString(config, "servername", "sni")
	setIfPresent(query, "sni", sni)
	setIfPresent(query, "fp", stringValue(config, "client-fingerprint"))
	setIfPresent(query, "pcs", stringValue(config, "fingerprint"))
	setIfPresent(query, "alpn", strings.Join(stringSlice(config["alpn"]), ","))
	if boolValue(config, "skip-cert-verify") {
		query.Set("allowInsecure", "1")
	}
	addTransportOptions(query, config, network)

	return scheme + "://" + url.User(credential).String() + "@" + hostPort(server, port) +
		"?" + query.Encode() + "#" + escapeFragment(stringValue(config, "name")), nil
}

func generateVMess(config map[string]any) (string, error) {
	server, port, err := endpoint(config)
	if err != nil {
		return "", err
	}
	uuid := stringValue(config, "uuid")
	if uuid == "" {
		return "", fmt.Errorf("vmess uuid is empty")
	}

	network := strings.ToLower(stringValue(config, "network"))
	if network == "" {
		network = "tcp"
	}
	values := map[string]any{
		"v":    "2",
		"ps":   stringValue(config, "name"),
		"add":  server,
		"port": port,
		"id":   uuid,
		"aid":  numberOrString(config["alterId"], "0"),
		"scy":  firstString(config, "cipher", "security"),
		"net":  network,
		"type": "none",
		"host": transportHost(config, network),
		"path": transportPath(config, network),
		"tls":  ternary(boolValue(config, "tls"), "tls", ""),
		"sni":  firstString(config, "servername", "sni"),
		"alpn": strings.Join(stringSlice(config["alpn"]), ","),
		"fp":   stringValue(config, "client-fingerprint"),
	}
	if network == "http" {
		values["type"] = "http"
	}
	body, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(body), nil
}

func generateSS(config map[string]any) (string, error) {
	server, port, err := endpoint(config)
	if err != nil {
		return "", err
	}
	cipher := stringValue(config, "cipher")
	password := stringValue(config, "password")
	if cipher == "" || password == "" {
		return "", fmt.Errorf("ss cipher or password is empty")
	}
	credential := base64.RawURLEncoding.EncodeToString([]byte(cipher + ":" + password))
	query := url.Values{}
	plugin := stringValue(config, "plugin")
	if plugin != "" {
		opts := mapValue(config, "plugin-opts")
		parts := []string{plugin}
		for _, key := range []string{"mode", "host", "path"} {
			if value := stringValue(opts, key); value != "" {
				parts = append(parts, key+"="+value)
			}
		}
		if boolValue(opts, "tls") {
			parts = append(parts, "tls")
		}
		query.Set("plugin", strings.Join(parts, ";"))
	}
	if boolValue(config, "udp-over-tcp") {
		query.Set("uot", "1")
	}
	result := "ss://" + credential + "@" + hostPort(server, port)
	if encoded := query.Encode(); encoded != "" {
		result += "?" + encoded
	}
	return result + "#" + escapeFragment(stringValue(config, "name")), nil
}

func generateSSR(config map[string]any) (string, error) {
	server, port, err := endpoint(config)
	if err != nil {
		return "", err
	}
	protocol := stringValue(config, "protocol")
	cipher := stringValue(config, "cipher")
	obfs := stringValue(config, "obfs")
	password := stringValue(config, "password")
	if protocol == "" || cipher == "" || obfs == "" || password == "" {
		return "", fmt.Errorf("ssr protocol parameters are incomplete")
	}
	query := url.Values{}
	setBase64Query(query, "remarks", stringValue(config, "name"))
	setBase64Query(query, "obfsparam", stringValue(config, "obfs-param"))
	setBase64Query(query, "protoparam", stringValue(config, "protocol-param"))
	if boolValue(config, "udp-over-tcp") {
		query.Set("uot", "1")
	}
	body := server + ":" + port + ":" + protocol + ":" + cipher + ":" + obfs + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(password)) + "/?" + query.Encode()
	return "ssr://" + base64.RawURLEncoding.EncodeToString([]byte(body)), nil
}

func generateHysteria2(config map[string]any) (string, error) {
	server, port, err := endpoint(config)
	if err != nil {
		return "", err
	}
	password := stringValue(config, "password")
	if password == "" {
		return "", fmt.Errorf("hysteria2 password is empty")
	}
	query := url.Values{}
	setIfPresent(query, "sni", firstString(config, "sni", "servername"))
	setIfPresent(query, "obfs", stringValue(config, "obfs"))
	setIfPresent(query, "obfs-password", stringValue(config, "obfs-password"))
	setIfPresent(query, "pinSHA256", stringValue(config, "fingerprint"))
	setIfPresent(query, "alpn", strings.Join(stringSlice(config["alpn"]), ","))
	setIfPresent(query, "up", stringValue(config, "up"))
	setIfPresent(query, "down", stringValue(config, "down"))
	if boolValue(config, "skip-cert-verify") {
		query.Set("insecure", "1")
	}
	return "hysteria2://" + url.User(password).String() + "@" + hostPort(server, port) +
		"?" + query.Encode() + "#" + escapeFragment(stringValue(config, "name")), nil
}

func generateTUIC(config map[string]any) (string, error) {
	server, port, err := endpoint(config)
	if err != nil {
		return "", err
	}
	credential := ""
	if token := stringValue(config, "token"); token != "" {
		credential = url.User(token).String()
	}
	if uuid := stringValue(config, "uuid"); uuid != "" {
		password := stringValue(config, "password")
		if password == "" {
			return "", fmt.Errorf("tuic password is empty")
		}
		credential = url.UserPassword(uuid, password).String()
	}
	if credential == "" {
		return "", fmt.Errorf("tuic credential is empty")
	}
	query := url.Values{}
	setIfPresent(query, "sni", firstString(config, "sni", "servername"))
	setIfPresent(query, "alpn", strings.Join(stringSlice(config["alpn"]), ","))
	setIfPresent(query, "congestion_control", stringValue(config, "congestion-controller"))
	setIfPresent(query, "udp_relay_mode", stringValue(config, "udp-relay-mode"))
	if boolValue(config, "disable-sni") {
		query.Set("disable_sni", "1")
	}
	return "tuic://" + credential + "@" + hostPort(server, port) + "?" + query.Encode() +
		"#" + escapeFragment(stringValue(config, "name")), nil
}

func generateAnyTLS(config map[string]any) (string, error) {
	server, port, err := endpoint(config)
	if err != nil {
		return "", err
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", fmt.Errorf("anytls port is invalid: %s", port)
	}
	password := stringValue(config, "password")
	if password == "" {
		return "", fmt.Errorf("anytls password is empty")
	}

	// The AnyTLS URI format understood by Mihomo can preserve only these
	// connection fields. Refuse non-empty extensions instead of producing a
	// link that imports successfully but behaves differently from the source.
	allowed := map[string]struct{}{
		"name": {}, "type": {}, "server": {}, "port": {}, "username": {},
		"password": {}, "sni": {}, "fingerprint": {},
		"skip-cert-verify": {}, "udp": {},
	}
	for key, value := range config {
		if _, ok := allowed[key]; !ok && meaningfulValue(value) {
			return "", fmt.Errorf("anytls share URL cannot preserve field: %s", key)
		}
	}
	if value, exists := config["udp"]; exists && !boolScalar(value) {
		return "", fmt.Errorf("anytls share URL cannot preserve udp=false")
	}

	query := url.Values{}
	setIfPresent(query, "sni", firstString(config, "sni", "servername"))
	setIfPresent(query, "hpkp", stringValue(config, "fingerprint"))
	if boolValue(config, "skip-cert-verify") {
		query.Set("insecure", "1")
	}

	result := "anytls://" + url.User(password).String() + "@" + hostPort(server, port)
	if encoded := query.Encode(); encoded != "" {
		result += "?" + encoded
	}
	result += "#" + escapeFragment(stringValue(config, "name"))
	parsed, err := url.Parse(result)
	if err != nil || parsed.Hostname() == "" || parsed.Port() == "" {
		return "", fmt.Errorf("invalid anytls endpoint")
	}
	return result, nil
}

func addTransportOptions(query url.Values, config map[string]any, network string) {
	setIfPresent(query, "host", transportHost(config, network))
	setIfPresent(query, "path", transportPath(config, network))
	switch network {
	case "ws", "httpupgrade":
		opts := mapValue(config, "ws-opts")
		if value := stringValue(opts, "max-early-data"); value != "" && value != "0" {
			query.Set("ed", value)
		}
		setIfPresent(query, "eh", stringValue(opts, "early-data-header-name"))
	case "grpc":
		opts := mapValue(config, "grpc-opts")
		setIfPresent(query, "serviceName", stringValue(opts, "grpc-service-name"))
	}
}

func transportHost(config map[string]any, network string) string {
	var value any
	switch network {
	case "ws", "httpupgrade":
		value = mapValue(mapValue(config, "ws-opts"), "headers")["Host"]
	case "grpc":
		return ""
	case "h2":
		value = mapValue(config, "h2-opts")["host"]
	case "http":
		value = mapValue(mapValue(config, "http-opts"), "headers")["Host"]
	case "xhttp":
		value = mapValue(config, "xhttp-opts")["host"]
	}
	values := stringSlice(value)
	if len(values) > 0 {
		return values[0]
	}
	return scalarString(value)
}

func transportPath(config map[string]any, network string) string {
	var value any
	switch network {
	case "ws", "httpupgrade":
		value = mapValue(config, "ws-opts")["path"]
	case "grpc":
		value = mapValue(config, "grpc-opts")["grpc-service-name"]
	case "h2":
		value = mapValue(config, "h2-opts")["path"]
	case "http":
		value = mapValue(config, "http-opts")["path"]
	case "xhttp":
		value = mapValue(config, "xhttp-opts")["path"]
	}
	values := stringSlice(value)
	if len(values) > 0 {
		return values[0]
	}
	return scalarString(value)
}

func endpoint(config map[string]any) (string, string, error) {
	server := stringValue(config, "server")
	port := stringValue(config, "port")
	if server == "" || port == "" {
		return "", "", fmt.Errorf("server or port is empty")
	}
	return server, port, nil
}

func hostPort(server, port string) string {
	return net.JoinHostPort(strings.Trim(server, "[]"), port)
}

func escapeFragment(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func setIfPresent(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

func setBase64Query(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, base64.RawURLEncoding.EncodeToString([]byte(value)))
	}
}

func stringValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	return scalarString(values[key])
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func boolValue(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	switch typed := values[key].(type) {
	case bool:
		return typed
	case string:
		value, _ := strconv.ParseBool(typed)
		return value
	default:
		return false
	}
}

func boolScalar(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(typed)
		return err == nil && parsed
	default:
		return false
	}
}

func meaningfulValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case uint64:
		return typed != 0
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case json.Number:
		return typed.String() != "" && typed.String() != "0"
	case []string:
		return len(typed) != 0
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	case map[any]any:
		return len(typed) != 0
	default:
		return true
	}
}

func mapValue(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	switch typed := values[key].(type) {
	case map[string]any:
		return typed
	case map[any]any:
		result := make(map[string]any, len(typed))
		for rawKey, value := range typed {
			key, ok := rawKey.(string)
			if ok {
				result[key] = value
			}
		}
		return result
	default:
		return nil
	}
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := scalarString(item); text != "" {
				result = append(result, text)
			}
		}
		return result
	case string:
		if typed != "" {
			return []string{typed}
		}
	}
	return nil
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values, key); value != "" {
			return value
		}
	}
	return ""
}

func numberOrString(value any, fallback string) string {
	if text := scalarString(value); text != "" {
		return text
	}
	return fallback
}

func ternary(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

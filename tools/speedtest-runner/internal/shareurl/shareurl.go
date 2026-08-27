package shareurl

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/metacubex/mihomo/common/convert"
)

// Generate builds a share URL directly from a Mihomo proxy configuration, then
// imports it through the same Mihomo converter used by the production
// subscription parser. A link is returned only when exactly one node is
// imported and every connection-relevant field remains semantically equal.
func Generate(config map[string]any) (string, error) {
	link, err := generateRaw(config)
	if err != nil {
		return "", err
	}
	if err := validateGeneratedURL(config, link); err != nil {
		return "", err
	}
	return link, nil
}

func generateRaw(config map[string]any) (string, error) {
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

type semanticFieldError struct {
	field string
}

func (err *semanticFieldError) Error() string {
	return "invalid connection field: " + err.field
}

func validateGeneratedURL(config map[string]any, link string) error {
	protocol := canonicalProtocol(stringValue(config, "type"))
	if protocol == "" {
		protocol = "proxy"
	}
	if link == "" || strings.ContainsAny(link, "\r\n") {
		return fmt.Errorf("%s share URL is not a single production-importable node", protocol)
	}

	proxies, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil {
		return fmt.Errorf("%s share URL is not accepted by the production importer", protocol)
	}
	return validateImportedProxies(protocol, config, proxies)
}

func validateImportedProxies(protocol string, config map[string]any, proxies []map[string]any) error {
	if len(proxies) != 1 {
		return fmt.Errorf("%s share URL production import returned %d nodes; expected exactly one", protocol, len(proxies))
	}

	expected, err := connectionSemantics(config)
	if err != nil {
		return semanticValidationError(protocol, err)
	}
	actual, err := connectionSemantics(proxies[0])
	if err != nil {
		return semanticValidationError(protocol, err)
	}
	if field := firstSemanticDifference(expected, actual); field != "" {
		return fmt.Errorf("%s share URL changes connection field: %s", protocol, field)
	}
	return nil
}

func semanticValidationError(protocol string, err error) error {
	if fieldError, ok := err.(*semanticFieldError); ok {
		return fmt.Errorf("%s share URL cannot normalize connection field: %s", protocol, fieldError.field)
	}
	return fmt.Errorf("%s share URL connection semantics are invalid", protocol)
}

func firstSemanticDifference(expected, actual map[string]string) string {
	keys := make([]string, 0, len(expected)+len(actual))
	seen := make(map[string]struct{}, len(expected)+len(actual))
	for key := range expected {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range actual {
		if _, exists := seen[key]; exists {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if expected[key] != actual[key] {
			return key
		}
	}
	return ""
}

func connectionSemantics(config map[string]any) (map[string]string, error) {
	protocol := canonicalProtocol(stringValue(config, "type"))
	if protocol == "" {
		return nil, &semanticFieldError{field: "type"}
	}
	semantics := make(map[string]string, 32)
	semantics["type"] = protocol
	semantics["server"] = canonicalHost(stringValue(config, "server"))
	if semantics["server"] == "" {
		return nil, &semanticFieldError{field: "server"}
	}
	port, err := canonicalUnsigned(config["port"], 0, 65535)
	if err != nil || port == "0" {
		return nil, &semanticFieldError{field: "port"}
	}
	semantics["port"] = port
	if err := addSemanticBool(semantics, config, "udp", true); err != nil {
		return nil, err
	}

	switch protocol {
	case "vless":
		semantics["uuid"] = strings.ToLower(strings.TrimSpace(stringValue(config, "uuid")))
		semantics["encryption"] = semanticStringDefault(config, "encryption", "none", true)
		semantics["flow"] = semanticStringDefault(config, "flow", "", true)
		reality := mapValue(config, "reality-opts")
		tls, err := semanticBool(config, "tls", false)
		if err != nil {
			return nil, &semanticFieldError{field: "tls"}
		}
		if meaningfulValue(reality) {
			tls = true
		}
		semantics["tls"] = strconv.FormatBool(tls)
		semantics["sni"] = canonicalHost(firstString(config, "servername", "sni"))
		clientFingerprint := strings.ToLower(strings.TrimSpace(stringValue(config, "client-fingerprint")))
		if clientFingerprint == "" && tls {
			clientFingerprint = "chrome"
		}
		semantics["client-fingerprint"] = clientFingerprint
		semantics["fingerprint"] = stringValue(config, "fingerprint")
		semantics["alpn"] = semanticStringList(config["alpn"], true, false)
		if err := addSemanticBool(semantics, config, "skip-cert-verify", false); err != nil {
			return nil, err
		}
		if err := addSemanticBool(semantics, config, "xudp", true); err != nil {
			return nil, err
		}
		semantics["reality-opts.public-key"] = stringValue(reality, "public-key")
		semantics["reality-opts.short-id"] = stringValue(reality, "short-id")
		network := semanticStringDefault(config, "network", "tcp", true)
		semantics["network"] = network
		if err := addTransportSemantics(semantics, config, protocol, network); err != nil {
			return nil, err
		}

	case "trojan":
		semantics["password"] = stringValue(config, "password")
		semantics["tls"] = "true"
		semantics["sni"] = canonicalHost(firstString(config, "servername", "sni"))
		clientFingerprint := strings.ToLower(strings.TrimSpace(stringValue(config, "client-fingerprint")))
		if clientFingerprint == "" {
			clientFingerprint = "chrome"
		}
		semantics["client-fingerprint"] = clientFingerprint
		semantics["fingerprint"] = stringValue(config, "fingerprint")
		semantics["alpn"] = semanticStringList(config["alpn"], true, false)
		if err := addSemanticBool(semantics, config, "skip-cert-verify", false); err != nil {
			return nil, err
		}
		reality := mapValue(config, "reality-opts")
		semantics["reality-opts.public-key"] = stringValue(reality, "public-key")
		semantics["reality-opts.short-id"] = stringValue(reality, "short-id")
		network := semanticStringDefault(config, "network", "tcp", true)
		semantics["network"] = network
		if err := addTransportSemantics(semantics, config, protocol, network); err != nil {
			return nil, err
		}

	case "vmess":
		semantics["uuid"] = strings.ToLower(strings.TrimSpace(stringValue(config, "uuid")))
		alterID, err := canonicalUnsignedDefault(config["alterId"], "0")
		if err != nil {
			return nil, &semanticFieldError{field: "alterId"}
		}
		semantics["alterId"] = alterID
		semantics["cipher"] = semanticStringDefaultAliases(config, []string{"cipher", "security"}, "auto", true)
		if err := addSemanticBool(semantics, config, "tls", false); err != nil {
			return nil, err
		}
		semantics["sni"] = canonicalHost(firstString(config, "servername", "sni"))
		semantics["alpn"] = semanticStringList(config["alpn"], true, false)
		if err := addSemanticBool(semantics, config, "skip-cert-verify", false); err != nil {
			return nil, err
		}
		if err := addSemanticBool(semantics, config, "xudp", true); err != nil {
			return nil, err
		}
		network := semanticStringDefault(config, "network", "tcp", true)
		semantics["network"] = network
		if err := addTransportSemantics(semantics, config, protocol, network); err != nil {
			return nil, err
		}

	case "ss":
		semantics["cipher"] = semanticStringDefault(config, "cipher", "", true)
		semantics["password"] = stringValue(config, "password")
		plugin := semanticStringDefault(config, "plugin", "", true)
		semantics["plugin"] = plugin
		pluginOptions := mapValue(config, "plugin-opts")
		switch plugin {
		case "obfs":
			semantics["plugin-opts.mode"] = semanticStringDefault(pluginOptions, "mode", "", true)
			semantics["plugin-opts.host"] = canonicalHost(stringValue(pluginOptions, "host"))
		case "v2ray-plugin":
			semantics["plugin-opts.mode"] = semanticStringDefault(pluginOptions, "mode", "", true)
			semantics["plugin-opts.host"] = canonicalHost(stringValue(pluginOptions, "host"))
			semantics["plugin-opts.path"] = stringValue(pluginOptions, "path")
			if err := addSemanticBoolFromKey(semantics, pluginOptions,
				"tls", "plugin-opts.tls", false); err != nil {
				return nil, err
			}
		}
		if err := addSemanticBoolFromKey(semantics, config, "udp-over-tcp", "udp-over-tcp", false); err != nil {
			return nil, err
		}

	case "ssr":
		semantics["protocol"] = semanticStringDefault(config, "protocol", "", true)
		semantics["cipher"] = semanticStringDefault(config, "cipher", "", true)
		semantics["obfs"] = semanticStringDefault(config, "obfs", "", true)
		semantics["password"] = stringValue(config, "password")
		semantics["obfs-param"] = stringValue(config, "obfs-param")
		semantics["protocol-param"] = stringValue(config, "protocol-param")
		if err := addSemanticBoolFromKey(semantics, config, "udp-over-tcp", "udp-over-tcp", false); err != nil {
			return nil, err
		}

	case "hysteria2":
		semantics["password"] = stringValue(config, "password")
		semantics["sni"] = canonicalHost(firstString(config, "sni", "servername"))
		semantics["obfs"] = semanticStringDefault(config, "obfs", "", true)
		semantics["obfs-password"] = stringValue(config, "obfs-password")
		semantics["fingerprint"] = stringValue(config, "fingerprint")
		semantics["alpn"] = semanticStringList(config["alpn"], true, false)
		semantics["up"] = stringValue(config, "up")
		semantics["down"] = stringValue(config, "down")
		if err := addSemanticBool(semantics, config, "skip-cert-verify", false); err != nil {
			return nil, err
		}

	case "tuic":
		semantics["token"] = stringValue(config, "token")
		semantics["uuid"] = strings.ToLower(strings.TrimSpace(stringValue(config, "uuid")))
		semantics["password"] = stringValue(config, "password")
		semantics["sni"] = canonicalHost(firstString(config, "sni", "servername"))
		semantics["alpn"] = semanticStringList(config["alpn"], true, false)
		semantics["congestion-controller"] = semanticStringDefault(config, "congestion-controller", "", true)
		semantics["udp-relay-mode"] = semanticStringDefault(config, "udp-relay-mode", "", true)
		if err := addSemanticBool(semantics, config, "disable-sni", false); err != nil {
			return nil, err
		}

	case "anytls":
		semantics["password"] = stringValue(config, "password")
		semantics["sni"] = canonicalHost(firstString(config, "sni", "servername"))
		semantics["fingerprint"] = stringValue(config, "fingerprint")
		if err := addSemanticBool(semantics, config, "skip-cert-verify", false); err != nil {
			return nil, err
		}

	default:
		return nil, &semanticFieldError{field: "type"}
	}

	return semantics, nil
}

func addTransportSemantics(
	semantics map[string]string,
	config map[string]any,
	protocol string,
	network string,
) error {
	switch network {
	case "tcp":
		return nil
	case "ws", "httpupgrade":
		opts := mapValue(config, "ws-opts")
		defaultPath := ""
		if protocol == "vmess" {
			defaultPath = "/"
		}
		semantics["ws-opts.path"] = semanticStringDefault(opts, "path", defaultPath, false)
		semantics["ws-opts.headers.Host"] = semanticHostHeaderList(opts["headers"])
		maxEarlyData, err := canonicalUnsignedDefault(opts["max-early-data"], "0")
		if err != nil {
			return &semanticFieldError{field: "ws-opts.max-early-data"}
		}
		semantics["ws-opts.max-early-data"] = maxEarlyData
		semantics["ws-opts.early-data-header-name"] = stringValue(opts, "early-data-header-name")
		if err := addSemanticBoolFromKey(semantics, opts,
			"v2ray-http-upgrade-fast-open", "ws-opts.v2ray-http-upgrade-fast-open", false); err != nil {
			return err
		}
		return nil
	case "grpc":
		opts := mapValue(config, "grpc-opts")
		semantics["grpc-opts.grpc-service-name"] = stringValue(opts, "grpc-service-name")
		return nil
	case "h2":
		opts := mapValue(config, "h2-opts")
		semantics["h2-opts.path"] = semanticStringDefault(opts, "path", "/", false)
		semantics["h2-opts.host"] = semanticStringList(opts["host"], false, true)
		return nil
	case "http":
		opts := mapValue(config, "http-opts")
		paths := semanticStringValues(opts["path"], false)
		if len(paths) == 0 {
			paths = []string{"/"}
		}
		semantics["http-opts.path"] = encodeSemanticList(paths)
		semantics["http-opts.headers.Host"] = semanticHostHeaderList(opts["headers"])
		return nil
	case "xhttp":
		opts := mapValue(config, "xhttp-opts")
		semantics["xhttp-opts.path"] = stringValue(opts, "path")
		semantics["xhttp-opts.host"] = semanticStringList(opts["host"], false, true)
		return nil
	default:
		return &semanticFieldError{field: "network"}
	}
}

func canonicalProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "vless":
		return "vless"
	case "trojan":
		return "trojan"
	case "vmess":
		return "vmess"
	case "ss":
		return "ss"
	case "ssr":
		return "ssr"
	case "hysteria2", "hy2":
		return "hysteria2"
	case "tuic":
		return "tuic"
	case "anytls":
		return "anytls"
	default:
		return ""
	}
}

func canonicalHost(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' {
		value = value[1 : len(value)-1]
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return strings.ToLower(value)
}

func canonicalUnsigned(value any, minimum, maximum uint64) (string, error) {
	text := strings.TrimSpace(scalarString(value))
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		return "", fmt.Errorf("invalid unsigned integer")
	}
	return strconv.FormatUint(parsed, 10), nil
}

func canonicalUnsignedDefault(value any, fallback string) (string, error) {
	if strings.TrimSpace(scalarString(value)) == "" {
		return fallback, nil
	}
	return canonicalUnsigned(value, 0, ^uint64(0))
}

func semanticBool(config map[string]any, key string, fallback bool) (bool, error) {
	if config == nil {
		return fallback, nil
	}
	value, exists := config[key]
	if !exists || value == nil {
		return fallback, nil
	}
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		text := strings.ToLower(strings.TrimSpace(typed))
		if text == "" {
			return fallback, nil
		}
		if text == "true" || text == "1" {
			return true, nil
		}
		if text == "false" || text == "0" {
			return false, nil
		}
		return false, fmt.Errorf("invalid boolean")
	default:
		text := strings.TrimSpace(scalarString(value))
		if text == "1" {
			return true, nil
		}
		if text == "0" {
			return false, nil
		}
		return false, fmt.Errorf("invalid boolean")
	}
}

func addSemanticBool(semantics map[string]string, config map[string]any, key string, fallback bool) error {
	return addSemanticBoolFromKey(semantics, config, key, key, fallback)
}

func addSemanticBoolFromKey(
	semantics map[string]string,
	config map[string]any,
	configKey string,
	semanticKey string,
	fallback bool,
) error {
	value, err := semanticBool(config, configKey, fallback)
	if err != nil {
		return &semanticFieldError{field: semanticKey}
	}
	semantics[semanticKey] = strconv.FormatBool(value)
	return nil
}

func semanticStringDefault(config map[string]any, key, fallback string, lower bool) string {
	value := strings.TrimSpace(stringValue(config, key))
	if value == "" {
		value = fallback
	}
	if lower {
		value = strings.ToLower(value)
	}
	return value
}

func semanticStringDefaultAliases(config map[string]any, keys []string, fallback string, lower bool) string {
	value := strings.TrimSpace(firstString(config, keys...))
	if value == "" {
		value = fallback
	}
	if lower {
		value = strings.ToLower(value)
	}
	return value
}

func semanticStringList(value any, splitComma bool, hosts bool) string {
	values := semanticStringValues(value, splitComma)
	if hosts {
		for index := range values {
			values[index] = canonicalHost(values[index])
		}
	}
	return encodeSemanticList(values)
}

func semanticStringValues(value any, splitComma bool) []string {
	values := stringSlice(value)
	if splitComma && len(values) == 1 && strings.Contains(values[0], ",") {
		values = strings.Split(values[0], ",")
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func semanticHostHeaderList(raw any) string {
	headers := anyMap(raw)
	if headers == nil {
		return "[]"
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		if strings.EqualFold(key, "host") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	values := make([]string, 0)
	for _, key := range keys {
		for _, value := range semanticStringValues(headers[key], false) {
			values = append(values, canonicalHost(value))
		}
	}
	return encodeSemanticList(values)
}

func encodeSemanticList(values []string) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func generateV(config map[string]any, scheme, credentialKey string) (string, error) {
	allowed := []string{
		"name", "type", "server", "port", credentialKey, "network", "tls",
		"servername", "sni", "client-fingerprint", "fingerprint", "alpn",
		"reality-opts", "ws-opts", "grpc-opts", "h2-opts",
		"http-opts", "xhttp-opts", "udp",
	}
	if scheme == "vless" {
		allowed = append(allowed, "encryption", "flow")
	} else {
		allowed = append(allowed, "skip-cert-verify")
	}
	if err := validateLosslessFields(config, scheme, allowed...); err != nil {
		return "", err
	}
	if err := validateAliasPair(config, scheme, "servername", "sni"); err != nil {
		return "", err
	}
	if scheme == "trojan" {
		if value, exists := config["tls"]; exists && booleanIsExplicitlyFalseOrInvalid(value) {
			return "", fmt.Errorf("trojan share URL cannot preserve tls=false")
		}
	}
	if err := validateRealityOptions(config, scheme); err != nil {
		return "", err
	}

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
	if err := validateTransportOptions(config, scheme, network, false); err != nil {
		return "", err
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
	if err := validateLosslessFields(config, "vmess",
		"name", "type", "server", "port", "uuid", "alterId", "cipher", "security",
		"network", "tls", "servername", "sni", "alpn",
		"ws-opts", "grpc-opts", "h2-opts", "http-opts", "xhttp-opts", "udp",
	); err != nil {
		return "", err
	}
	if err := validateAliasPair(config, "vmess", "servername", "sni"); err != nil {
		return "", err
	}
	if err := validateAliasPair(config, "vmess", "cipher", "security"); err != nil {
		return "", err
	}
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
	if err := validateTransportOptions(config, "vmess", network, true); err != nil {
		return "", err
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
	if err := validateLosslessFields(config, "ss",
		"name", "type", "server", "port", "cipher", "password", "plugin",
		"plugin-opts", "udp-over-tcp", "udp",
	); err != nil {
		return "", err
	}
	if err := validateSSPluginOptions(config); err != nil {
		return "", err
	}
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
		if strings.EqualFold(plugin, "obfs") {
			if value := stringValue(opts, "mode"); value != "" {
				parts = append(parts, "obfs="+value)
			}
			if value := stringValue(opts, "host"); value != "" {
				parts = append(parts, "obfs-host="+value)
			}
		} else {
			for _, key := range []string{"mode", "host", "path"} {
				if value := stringValue(opts, key); value != "" {
					parts = append(parts, key+"="+value)
				}
			}
			if boolValue(opts, "tls") {
				parts = append(parts, "tls")
			}
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
	if err := validateLosslessFields(config, "ssr",
		"name", "type", "server", "port", "protocol", "cipher", "obfs", "password",
		"obfs-param", "protocol-param", "udp-over-tcp", "udp",
	); err != nil {
		return "", err
	}
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
	if err := validateLosslessFields(config, "hysteria2",
		"name", "type", "server", "port", "password", "sni", "servername", "obfs",
		"obfs-password", "fingerprint", "alpn", "up", "down", "skip-cert-verify", "udp",
	); err != nil {
		return "", err
	}
	if err := validateAliasPair(config, "hysteria2", "sni", "servername"); err != nil {
		return "", err
	}
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
	if err := validateLosslessFields(config, "tuic",
		"name", "type", "server", "port", "token", "uuid", "password", "sni",
		"servername", "alpn", "congestion-controller", "udp-relay-mode", "disable-sni", "udp",
	); err != nil {
		return "", err
	}
	if err := validateAliasPair(config, "tuic", "sni", "servername"); err != nil {
		return "", err
	}
	if stringValue(config, "token") != "" && stringValue(config, "uuid") != "" {
		return "", fmt.Errorf("tuic share URL cannot preserve token together with uuid/password")
	}
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
	if err := validateLosslessFields(config, "anytls",
		"name", "type", "server", "port", "password", "sni", "servername", "fingerprint",
		"skip-cert-verify", "udp",
	); err != nil {
		return "", err
	}
	if err := validateAliasPair(config, "anytls", "sni", "servername"); err != nil {
		return "", err
	}
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

func validateLosslessFields(config map[string]any, protocol string, fields ...string) error {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for key, value := range config {
		if _, ok := allowed[key]; !ok && meaningfulValue(value) {
			return fmt.Errorf("%s share URL cannot preserve field: %s", protocol, key)
		}
	}
	if value, exists := config["udp"]; exists && booleanIsExplicitlyFalseOrInvalid(value) {
		return fmt.Errorf("%s share URL cannot preserve udp=false", protocol)
	}
	return nil
}

func validateAliasPair(config map[string]any, protocol, first, second string) error {
	firstValue := stringValue(config, first)
	secondValue := stringValue(config, second)
	if firstValue != "" && secondValue != "" && firstValue != secondValue {
		return fmt.Errorf("%s share URL cannot preserve conflicting %s and %s", protocol, first, second)
	}
	return nil
}

func validateRealityOptions(config map[string]any, protocol string) error {
	return validateNestedFields(config, protocol, "reality-opts", "public-key", "short-id")
}

func validateTransportOptions(config map[string]any, protocol, network string, vmess bool) error {
	optionFields := []string{"ws-opts", "grpc-opts", "h2-opts", "http-opts", "xhttp-opts"}
	expected := ""
	switch network {
	case "tcp":
	case "ws", "httpupgrade":
		expected = "ws-opts"
	case "grpc":
		expected = "grpc-opts"
	case "h2":
		expected = "h2-opts"
	case "http":
		expected = "http-opts"
	case "xhttp":
		expected = "xhttp-opts"
	default:
		return fmt.Errorf("%s share URL cannot preserve network: %s", protocol, network)
	}
	for _, field := range optionFields {
		if field != expected && meaningfulValue(config[field]) {
			return fmt.Errorf("%s share URL cannot preserve field %s for network %s", protocol, field, network)
		}
	}

	switch expected {
	case "ws-opts":
		allowed := []string{"path", "headers"}
		if !vmess {
			allowed = append(allowed, "max-early-data", "early-data-header-name")
		}
		if err := validateNestedFields(config, protocol, expected, allowed...); err != nil {
			return err
		}
		return validateHostHeaders(mapValue(config, expected)["headers"], protocol, expected)
	case "grpc-opts":
		return validateNestedFields(config, protocol, expected, "grpc-service-name")
	case "h2-opts":
		return validateNestedFields(config, protocol, expected, "host", "path")
	case "http-opts":
		if err := validateNestedFields(config, protocol, expected, "headers", "path"); err != nil {
			return err
		}
		return validateHostHeaders(mapValue(config, expected)["headers"], protocol, expected)
	case "xhttp-opts":
		return validateNestedFields(config, protocol, expected, "host", "path")
	default:
		return nil
	}
}

func validateSSPluginOptions(config map[string]any) error {
	plugin := strings.ToLower(stringValue(config, "plugin"))
	if plugin == "" && meaningfulValue(config["plugin-opts"]) {
		return fmt.Errorf("ss share URL cannot preserve plugin-opts without a plugin")
	}
	switch plugin {
	case "":
		return nil
	case "obfs":
		return validateNestedFields(config, "ss", "plugin-opts", "mode", "host")
	case "v2ray-plugin":
		return validateNestedFields(config, "ss", "plugin-opts", "mode", "host", "path", "tls")
	default:
		return fmt.Errorf("ss share URL cannot preserve the configured plugin")
	}
}

func validateNestedFields(config map[string]any, protocol, field string, allowedFields ...string) error {
	raw, exists := config[field]
	if !exists || !meaningfulValue(raw) {
		return nil
	}
	nested := mapValue(config, field)
	if nested == nil {
		return fmt.Errorf("%s share URL cannot preserve malformed field: %s", protocol, field)
	}
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, allowedField := range allowedFields {
		allowed[allowedField] = struct{}{}
	}
	for key, value := range nested {
		if _, ok := allowed[key]; !ok && meaningfulValue(value) {
			return fmt.Errorf("%s share URL cannot preserve field: %s.%s", protocol, field, key)
		}
	}
	return nil
}

func validateHostHeaders(raw any, protocol, field string) error {
	headers := anyMap(raw)
	if headers == nil {
		if meaningfulValue(raw) {
			return fmt.Errorf("%s share URL cannot preserve malformed field: %s.headers", protocol, field)
		}
		return nil
	}
	host := ""
	for key, value := range headers {
		if !strings.EqualFold(key, "host") && meaningfulValue(value) {
			return fmt.Errorf("%s share URL cannot preserve field: %s.headers.%s", protocol, field, key)
		}
		if strings.EqualFold(key, "host") {
			current := scalarString(value)
			if host != "" && current != "" && host != current {
				return fmt.Errorf("%s share URL cannot preserve conflicting Host headers", protocol)
			}
			if current != "" {
				host = current
			}
		}
	}
	return nil
}

func transportHost(config map[string]any, network string) string {
	var value any
	switch network {
	case "ws", "httpupgrade":
		value = hostHeaderValue(mapValue(config, "ws-opts")["headers"])
	case "grpc":
		return ""
	case "h2":
		value = mapValue(config, "h2-opts")["host"]
	case "http":
		value = hostHeaderValue(mapValue(config, "http-opts")["headers"])
	case "xhttp":
		value = mapValue(config, "xhttp-opts")["host"]
	}
	values := stringSlice(value)
	if len(values) > 0 {
		return values[0]
	}
	return scalarString(value)
}

func hostHeaderValue(raw any) any {
	for key, value := range anyMap(raw) {
		if strings.EqualFold(key, "host") {
			return value
		}
	}
	return nil
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

func booleanIsExplicitlyFalseOrInvalid(value any) bool {
	if value == nil {
		return false
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
		return false
	}
	return !boolScalar(value)
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
		for _, item := range typed {
			if meaningfulValue(item) {
				return true
			}
		}
		return false
	case []any:
		for _, item := range typed {
			if meaningfulValue(item) {
				return true
			}
		}
		return false
	case map[string]any:
		for _, item := range typed {
			if meaningfulValue(item) {
				return true
			}
		}
		return false
	case map[any]any:
		for _, item := range typed {
			if meaningfulValue(item) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func anyMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[any]any:
		result := make(map[string]any, len(typed))
		for rawKey, item := range typed {
			key, ok := rawKey.(string)
			if ok {
				result[key] = item
			}
		}
		return result
	default:
		return nil
	}
}

func mapValue(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	return anyMap(values[key])
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

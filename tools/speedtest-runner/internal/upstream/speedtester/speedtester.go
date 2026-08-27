package speedtester

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	mihomoTransportHTTP "github.com/metacubex/http"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/common/convert"
	mihomoCA "github.com/metacubex/mihomo/component/ca"
	mihomoDialer "github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener/inner"
	"gopkg.in/yaml.v2"
)

type Config struct {
	ConfigPaths         string
	FilterRegex         string
	BlockRegex          string
	ServerURL           string
	DownloadSize        int
	ProbeTimeout        time.Duration
	DownloadTimeout     time.Duration
	Concurrent          int
	MaxLatency          time.Duration
	MaxHTTPProbeFailure float64
	MinDownloadSpeed    float64
	Mode                SpeedMode
	OutputPath          string
	UserAgent           string // optional; empty means use default (mihomo kernel UA)
}

type SourceOrigin string

const (
	SourceOriginLocal  SourceOrigin = "local"
	SourceOriginRemote SourceOrigin = "remote"
	SourceOriginInline SourceOrigin = "inline"

	PreparedSourceProtocolVersion = 1
	providerRegexMatchTimeout     = 250 * time.Millisecond
)

type ConfigSource struct {
	Path            string       `json:"path"`
	Origin          SourceOrigin `json:"origin"`
	LocalDependency string       `json:"local_dependency,omitempty"`
}

type httpConfigSizeError struct {
	limit int64
}

func (e *httpConfigSizeError) Error() string {
	return fmt.Sprintf("remote config exceeds %d bytes", e.limit)
}

type httpConfigStatusError struct {
	statusCode int
	summary    string
}

func (e *httpConfigStatusError) Error() string {
	return fmt.Sprintf("server returned %d %s: %s",
		e.statusCode, http.StatusText(e.statusCode), e.summary)
}

type providerNetworkBlockedError struct{}

func (*providerNetworkBlockedError) Error() string {
	return "private or local network destination"
}

type providerRedirectBlockedError struct {
	reason string
}

func (e *providerRedirectBlockedError) Error() string {
	return e.reason
}

type restrictedProviderDialer struct {
	lookupNetIP func(context.Context, string, string) ([]netip.Addr, error)
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func newRestrictedProviderDialer() constant.Dialer {
	return &restrictedProviderDialer{
		lookupNetIP: net.DefaultResolver.LookupNetIP,
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return mihomoDialer.DialContext(ctx, network, address)
		},
	}
}

func (d *restrictedProviderDialer) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid Provider destination: %w", err)
	}

	addresses := make([]netip.Addr, 0, 2)
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = append(addresses, literal)
	} else {
		addresses, err = d.lookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve Provider destination: %w", err)
		}
	}

	var lastDialErr error
	for _, candidate := range addresses {
		candidate = candidate.Unmap()
		if !isPublicProviderAddress(candidate) || !networkSupportsAddress(network, candidate) {
			continue
		}
		conn, dialErr := d.dialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastDialErr = dialErr
	}
	if lastDialErr != nil {
		return nil, lastDialErr
	}
	return nil, &providerNetworkBlockedError{}
}

func (d *restrictedProviderDialer) ListenPacket(
	context.Context,
	string,
	string,
	netip.AddrPort,
) (net.PacketConn, error) {
	return nil, fmt.Errorf("packet connections are not supported for Provider downloads")
}

func networkSupportsAddress(network string, address netip.Addr) bool {
	if strings.HasSuffix(network, "4") {
		return address.Is4()
	}
	if strings.HasSuffix(network, "6") {
		return address.Is6()
	}
	return true
}

func isPublicProviderAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() {
		return false
	}
	bestPrefixBits := -1
	globallyReachable := false
	for _, rule := range providerSpecialPurposeAddressRules {
		if rule.prefix.Contains(address) && rule.prefix.Bits() > bestPrefixBits {
			bestPrefixBits = rule.prefix.Bits()
			globallyReachable = rule.globallyReachable
		}
	}
	if bestPrefixBits >= 0 {
		return globallyReachable
	}
	return true
}

type providerSpecialPurposeAddressRule struct {
	prefix            netip.Prefix
	globallyReachable bool
}

// This table mirrors the IANA IPv4 and IPv6 Special-Purpose Address
// Registries last reviewed on 2025-10-09. Blank and N/A Globally Reachable
// values fail closed. Longest-prefix matching is required because both
// registries contain globally reachable exceptions inside broader blocks.
// IPv4-mapped IPv6 addresses are intentionally unmapped above and evaluated
// under their embedded IPv4 registry entry.
//
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
var providerSpecialPurposeAddressRules = []providerSpecialPurposeAddressRule{
	// IPv4 registry.
	{netip.MustParsePrefix("0.0.0.0/8"), false},
	{netip.MustParsePrefix("0.0.0.0/32"), false},
	{netip.MustParsePrefix("10.0.0.0/8"), false},
	{netip.MustParsePrefix("100.64.0.0/10"), false},
	{netip.MustParsePrefix("127.0.0.0/8"), false},
	{netip.MustParsePrefix("169.254.0.0/16"), false},
	{netip.MustParsePrefix("172.16.0.0/12"), false},
	{netip.MustParsePrefix("192.0.0.0/24"), false},
	{netip.MustParsePrefix("192.0.0.0/29"), false},
	{netip.MustParsePrefix("192.0.0.8/32"), false},
	{netip.MustParsePrefix("192.0.0.9/32"), true},
	{netip.MustParsePrefix("192.0.0.10/32"), true},
	{netip.MustParsePrefix("192.0.0.170/32"), false},
	{netip.MustParsePrefix("192.0.0.171/32"), false},
	{netip.MustParsePrefix("192.0.2.0/24"), false},
	{netip.MustParsePrefix("192.31.196.0/24"), true},
	{netip.MustParsePrefix("192.52.193.0/24"), true},
	{netip.MustParsePrefix("192.88.99.0/24"), false},
	{netip.MustParsePrefix("192.88.99.2/32"), false},
	{netip.MustParsePrefix("192.168.0.0/16"), false},
	{netip.MustParsePrefix("192.175.48.0/24"), true},
	{netip.MustParsePrefix("198.18.0.0/15"), false},
	{netip.MustParsePrefix("198.51.100.0/24"), false},
	{netip.MustParsePrefix("203.0.113.0/24"), false},
	{netip.MustParsePrefix("240.0.0.0/4"), false},
	{netip.MustParsePrefix("255.255.255.255/32"), false},

	// IPv6 registry. The ::ffff:0:0/96 entry is handled by Unmap above.
	{netip.MustParsePrefix("::/128"), false},
	{netip.MustParsePrefix("::1/128"), false},
	{netip.MustParsePrefix("64:ff9b::/96"), true},
	{netip.MustParsePrefix("64:ff9b:1::/48"), false},
	{netip.MustParsePrefix("100::/64"), false},
	{netip.MustParsePrefix("100:0:0:1::/64"), false},
	{netip.MustParsePrefix("2001::/23"), false},
	{netip.MustParsePrefix("2001::/32"), false},
	{netip.MustParsePrefix("2001:1::1/128"), true},
	{netip.MustParsePrefix("2001:1::2/128"), true},
	{netip.MustParsePrefix("2001:1::3/128"), true},
	{netip.MustParsePrefix("2001:2::/48"), false},
	{netip.MustParsePrefix("2001:3::/32"), true},
	{netip.MustParsePrefix("2001:4:112::/48"), true},
	{netip.MustParsePrefix("2001:10::/28"), false},
	{netip.MustParsePrefix("2001:20::/28"), true},
	{netip.MustParsePrefix("2001:30::/28"), true},
	{netip.MustParsePrefix("2001:db8::/32"), false},
	{netip.MustParsePrefix("2002::/16"), false},
	{netip.MustParsePrefix("2620:4f:8000::/48"), true},
	{netip.MustParsePrefix("3fff::/20"), false},
	{netip.MustParsePrefix("5f00::/16"), false},
	{netip.MustParsePrefix("fc00::/7"), false},
	{netip.MustParsePrefix("fe80::/10"), false},
}

type PreparedConfigSet struct {
	Version           int      `json:"version"`
	ConfigPaths       []string `json:"config_paths"`
	LocalDependencies []string `json:"local_dependencies"`
}

type serverMode int

const (
	serverModeDownloadServer serverMode = iota
	serverModeDirectDownload
)

const (
	maxHTTPConfigSize       = 32 * 1024 * 1024
	MaxTransferConcurrency  = 16
	latencyWarmupRequests   = 1
	latencyMeasuredRequests = 5
	latencyProbeInterval    = 100 * time.Millisecond
	configFetchTimeout      = 30 * time.Second
)

// defaultFetchConfigUA returns the default User-Agent (mihomo kernel format) when none is set.
func defaultFetchConfigUA() string {
	return constant.MihomoName + "/" + constant.Version
}

func (st *SpeedTester) fetchConfigUA() string {
	if st.config.UserAgent != "" {
		return st.config.UserAgent
	}
	return defaultFetchConfigUA()
}

func (st *SpeedTester) fetchHTTPConfig(targetURL string) ([]byte, error) {
	return st.fetchHTTPConfigWithOptions(targetURL, nil, maxHTTPConfigSize, nil, false)
}

func (st *SpeedTester) fetchHTTPConfigWithOptions(
	targetURL string,
	headers http.Header,
	maxSize int64,
	requestDialer constant.Dialer,
	secureProviderRedirects bool,
) ([]byte, error) {
	if maxSize <= 0 || maxSize > maxHTTPConfigSize {
		return nil, fmt.Errorf("remote config size limit must be between 1 and %d bytes", maxHTTPConfigSize)
	}
	requestHeaders := headers.Clone()
	if requestHeaders == nil {
		requestHeaders = make(http.Header)
	}
	if requestHeaders.Get("User-Agent") == "" {
		requestHeaders.Set("User-Agent", st.fetchConfigUA())
	}
	ctx, cancel := context.WithTimeout(context.Background(), configFetchTimeout)
	defer cancel()
	resp, cleanup, err := st.requestHTTPConfig(
		ctx, targetURL, requestHeaders, headers, requestDialer, secureProviderRedirects)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &httpConfigStatusError{
			statusCode: resp.StatusCode,
			summary:    trimHTTPError(string(detail)),
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxSize {
		return nil, &httpConfigSizeError{limit: maxSize}
	}
	return body, nil
}

func (st *SpeedTester) requestHTTPConfig(
	ctx context.Context,
	targetURL string,
	requestHeaders http.Header,
	userHeaders http.Header,
	requestDialer constant.Dialer,
	secureProviderRedirects bool,
) (*mihomoTransportHTTP.Response, func(), error) {
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, func() {}, err
	}
	req, err := mihomoTransportHTTP.NewRequestWithContext(
		ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, func() {}, err
	}
	for name, values := range requestHeaders {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if user := parsedURL.User; user != nil {
		password, _ := user.Password()
		req.SetBasicAuth(user.Username(), password)
	}

	tlsConfig, err := mihomoCA.GetTLSConfig(mihomoCA.Option{})
	if err != nil {
		return nil, func() {}, err
	}
	transport := &mihomoTransportHTTP.Transport{
		// Keep these values aligned with Mihomo component/http.HttpRequest.
		DisableKeepAlives:     runtime.GOOS == "android",
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if requestDialer != nil {
				return requestDialer.DialContext(ctx, network, address)
			}
			if conn, innerErr := inner.HandleTcp(inner.GetTunnel(), address, ""); innerErr == nil {
				return conn, nil
			}
			return mihomoDialer.DialContext(ctx, network, address)
		},
		TLSClientConfig: tlsConfig,
	}
	client := &mihomoTransportHTTP.Client{Transport: transport}
	if secureProviderRedirects {
		client.CheckRedirect = providerRedirectPolicy(
			parsedURL, len(userHeaders) > 0, st.fetchConfigUA())
	}
	resp, requestErr := client.Do(req)
	return resp, transport.CloseIdleConnections, requestErr
}

func providerRedirectPolicy(
	initialURL *url.URL,
	hasUserHeaders bool,
	defaultUserAgent string,
) func(*mihomoTransportHTTP.Request, []*mihomoTransportHTTP.Request) error {
	strictOrigin := hasUserHeaders || initialURL.User != nil ||
		initialURL.RawQuery != "" || initialURL.ForceQuery
	return func(req *mihomoTransportHTTP.Request, via []*mihomoTransportHTTP.Request) error {
		if len(via) >= 10 {
			return &providerRedirectBlockedError{reason: "stopped after 10 redirects"}
		}
		previous := via[len(via)-1]
		if strings.EqualFold(previous.URL.Scheme, "https") &&
			strings.EqualFold(req.URL.Scheme, "http") {
			return &providerRedirectBlockedError{reason: "HTTPS to HTTP downgrade"}
		}
		if !sameURLUserinfo(initialURL.User, req.URL.User) && req.URL.User != nil {
			return &providerRedirectBlockedError{reason: "redirect changed URL userinfo"}
		}
		if strictOrigin {
			if !sameProviderOrigin(initialURL, req.URL) {
				return &providerRedirectBlockedError{reason: "credentialed request changed origin"}
			}
			return nil
		}
		if !sameProviderOrigin(previous.URL, req.URL) {
			for name := range req.Header {
				req.Header.Del(name)
			}
			if defaultUserAgent != "" {
				req.Header.Set("User-Agent", defaultUserAgent)
			}
			req.Header.Del("Referer")
		}
		return nil
	}
}

func sameProviderOrigin(first *url.URL, second *url.URL) bool {
	return strings.EqualFold(first.Scheme, second.Scheme) &&
		strings.EqualFold(first.Hostname(), second.Hostname()) &&
		effectiveProviderPort(first) == effectiveProviderPort(second)
}

func effectiveProviderPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(value.Scheme, "http") {
		return "80"
	}
	return ""
}

func sameURLUserinfo(first *url.Userinfo, second *url.Userinfo) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.String() == second.String()
}

func trimHTTPError(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if value == "" {
		return "empty response body"
	}
	if len(value) > 300 {
		return value[:300] + "..."
	}
	return value
}

type serverTarget struct {
	mode        serverMode
	baseURL     string
	downloadURL string
}

type SpeedTester struct {
	config               *Config
	blockedNodes         []string
	blockedNodeCount     int
	serverMode           serverMode
	serverBaseURL        string
	downloadURL          string
	mode                 SpeedMode
	remoteProviderDialer constant.Dialer
}

func New(config *Config) (*SpeedTester, error) {
	if config.Concurrent <= 0 {
		config.Concurrent = 1
	}
	if config.Concurrent > MaxTransferConcurrency {
		return nil, fmt.Errorf("transfer concurrency must be between 1 and %d", MaxTransferConcurrency)
	}
	if config.DownloadSize < 0 {
		config.DownloadSize = 100 * 1024 * 1024
	}
	if config.ProbeTimeout <= 0 {
		config.ProbeTimeout = 3 * time.Second
	}
	if config.DownloadTimeout <= 0 {
		config.DownloadTimeout = 8 * time.Second
	}
	mode := config.Mode
	if mode == "" {
		mode = SpeedModeDownload
	}
	target, err := resolveServerTarget(config.ServerURL)
	if err != nil {
		return nil, err
	}
	config.Mode = mode
	return &SpeedTester{
		config:        config,
		serverMode:    target.mode,
		serverBaseURL: target.baseURL,
		downloadURL:   target.downloadURL,
		mode:          mode,
	}, nil
}

func (st *SpeedTester) Mode() SpeedMode {
	return st.mode
}

func resolveServerTarget(rawURL string) (*serverTarget, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("server url is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse server url %q failed: %w", rawURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("server url %q must include scheme and host", rawURL)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("server url %q must use http or https scheme, got %q", rawURL, parsed.Scheme)
	}
	path := strings.TrimSpace(parsed.Path)
	hasPath := strings.Trim(path, "/") != ""
	hasQuery := parsed.RawQuery != ""
	hasFragment := parsed.Fragment != ""
	if !hasPath && !hasQuery && !hasFragment {
		return &serverTarget{
			mode:    serverModeDownloadServer,
			baseURL: strings.TrimRight(trimmed, "/"),
		}, nil
	}
	return &serverTarget{
		mode:        serverModeDirectDownload,
		downloadURL: trimmed,
	}, nil
}

type CProxy struct {
	constant.Proxy
	Config map[string]any
}

type loadedProxy struct {
	name             string
	proxy            *CProxy
	sourceOrder      int
	sourceOrigin     SourceOrigin
	sourceIdentifier string
	providerName     string
	originalName     string
	originalConfig   map[string]any
}

type preparedProviderProxyConfig struct {
	originalName   string
	originalConfig map[string]any
	config         map[string]any
}

type RawConfig struct {
	Providers       map[string]map[string]any `yaml:"proxy-providers"`
	Proxies         []map[string]any          `yaml:"proxies"`
	ProviderPayload []map[string]any          `yaml:"payload"`
}

type providerSourceDefinition struct {
	typeName  string
	url       string
	path      string
	payload   any
	headers   http.Header
	sizeLimit int64
}

func PrepareConfigSources(
	sources []ConfigSource,
	outputDirectory string,
	userAgent string,
) (_ *PreparedConfigSet, err error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("source list is empty")
	}
	outputDirectory, err = filepath.Abs(strings.TrimSpace(outputDirectory))
	if err != nil {
		return nil, fmt.Errorf("resolve prepared output directory: %w", err)
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return nil, fmt.Errorf("create prepared output directory: %w", err)
	}

	tester := &SpeedTester{config: &Config{UserAgent: strings.TrimSpace(userAgent)}}
	preparedPaths := make([]string, 0, len(sources))
	createdPaths := make([]string, 0, len(sources))
	dependencies := make([]string, 0, len(sources))
	dependencyKeys := make(map[string]struct{})
	succeeded := false
	defer func() {
		if succeeded {
			return
		}
		for _, createdPath := range createdPaths {
			_ = os.Remove(createdPath)
		}
	}()

	addDependency := func(path string) error {
		absolute, absoluteErr := filepath.Abs(strings.TrimSpace(path))
		if absoluteErr != nil {
			return absoluteErr
		}
		absolute = filepath.Clean(absolute)
		key := absolute
		if filepath.Separator == '\\' {
			key = strings.ToLower(absolute)
		}
		if _, exists := dependencyKeys[key]; exists {
			return nil
		}
		dependencyKeys[key] = struct{}{}
		dependencies = append(dependencies, absolute)
		return nil
	}

	for index, rawSource := range sources {
		source, sourceErr := normalizeConfigSource(rawSource)
		if sourceErr != nil {
			return nil, fmt.Errorf("source %d: %w", index+1, sourceErr)
		}
		if source.Origin == SourceOriginLocal {
			if err := addDependency(source.LocalDependency); err != nil {
				return nil, fmt.Errorf("source %d local dependency: %w", index+1, err)
			}
		}

		body, readErr := os.ReadFile(source.Path)
		if readErr != nil {
			return nil, fmt.Errorf("source %d prepared config could not be read: %w", index+1, readErr)
		}
		rawConfig := &RawConfig{Proxies: []map[string]any{}}
		if decodeErr := yaml.Unmarshal(body, rawConfig); decodeErr != nil {
			return nil, fmt.Errorf("source %d prepared config could not be parsed: %w", index+1, decodeErr)
		}
		if validationErr := rejectUnsupportedDialerProxyConfigs(
			fmt.Sprintf("source %d top-level", index+1), rawConfig.Proxies); validationErr != nil {
			return nil, validationErr
		}

		providerNames := make([]string, 0, len(rawConfig.Providers))
		for name := range rawConfig.Providers {
			providerNames = append(providerNames, name)
		}
		sort.Strings(providerNames)
		for _, name := range providerNames {
			providerConfig := rawConfig.Providers[name]
			providerConfigs, dependency, loadErr := tester.loadProviderProxyConfigsFromSource(
				source, name, providerConfig)
			if loadErr != nil {
				return nil, loadErr
			}
			if dependency != "" {
				if err := addDependency(dependency); err != nil {
					return nil, fmt.Errorf("proxy provider %s dependency: %w", name, err)
				}
			}

			inlineConfig := cloneProxyConfig(providerConfig)
			inlineConfig["type"] = "inline"
			inlineConfig["payload"] = providerConfigs
			for _, key := range []string{
				"url", "path", "header", "proxy", "size-limit", "age-secret-key",
			} {
				delete(inlineConfig, key)
			}
			rawConfig.Providers[name] = inlineConfig
		}

		materialized, marshalErr := yaml.Marshal(rawConfig)
		if marshalErr != nil {
			return nil, fmt.Errorf("source %d prepared config could not be encoded: %w", index+1, marshalErr)
		}
		preparedPath := filepath.Join(outputDirectory, fmt.Sprintf("materialized-%03d.yaml", index+1))
		file, openErr := os.OpenFile(preparedPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			return nil, fmt.Errorf("source %d prepared config could not be created: %w", index+1, openErr)
		}
		createdPaths = append(createdPaths, preparedPath)
		if _, writeErr := file.Write(materialized); writeErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("source %d prepared config could not be written: %w", index+1, writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("source %d prepared config could not be closed: %w", index+1, closeErr)
		}
		preparedPaths = append(preparedPaths, preparedPath)
	}

	sort.Strings(dependencies)
	succeeded = true
	return &PreparedConfigSet{
		Version:           PreparedSourceProtocolVersion,
		ConfigPaths:       preparedPaths,
		LocalDependencies: dependencies,
	}, nil
}

func normalizeConfigSource(source ConfigSource) (ConfigSource, error) {
	source.Path = strings.TrimSpace(source.Path)
	if source.Path == "" {
		return ConfigSource{}, fmt.Errorf("prepared config path is empty")
	}
	absolutePath, err := filepath.Abs(source.Path)
	if err != nil {
		return ConfigSource{}, fmt.Errorf("resolve prepared config path: %w", err)
	}
	source.Path = filepath.Clean(absolutePath)
	source.LocalDependency = strings.TrimSpace(source.LocalDependency)

	switch source.Origin {
	case SourceOriginLocal:
		if source.LocalDependency == "" {
			return ConfigSource{}, fmt.Errorf("local source dependency is empty")
		}
		absoluteDependency, dependencyErr := filepath.Abs(source.LocalDependency)
		if dependencyErr != nil {
			return ConfigSource{}, fmt.Errorf("resolve local source dependency: %w", dependencyErr)
		}
		source.LocalDependency = filepath.Clean(absoluteDependency)
	case SourceOriginRemote, SourceOriginInline:
		if source.LocalDependency != "" {
			return ConfigSource{}, fmt.Errorf("%s source must not declare a local dependency", source.Origin)
		}
	default:
		return ConfigSource{}, fmt.Errorf("unsupported source origin %q", source.Origin)
	}
	return source, nil
}

func (st *SpeedTester) LoadProxies() (map[string]*CProxy, error) {
	loadedProxies := make([]loadedProxy, 0)
	st.blockedNodes = make([]string, 0)
	st.blockedNodeCount = 0

	sourceOrder := 0
	for configPath := range strings.SplitSeq(st.config.ConfigPaths, ",") {
		sourceOrder++
		configPath = strings.TrimSpace(configPath)
		if configPath == "" {
			return nil, fmt.Errorf("config path is empty")
		}
		source := ConfigSource{Path: configPath}
		var body []byte
		var err error
		if isHTTPURL(configPath) {
			source.Origin = SourceOriginRemote
			body, err = st.fetchHTTPConfig(configPath)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch remote config")
			}
		} else {
			source.Origin = SourceOriginLocal
			source.LocalDependency = configPath
			body, err = os.ReadFile(configPath)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read config %s: %w", configPath, err)
		}

		rawCfg := &RawConfig{
			Proxies: []map[string]any{},
		}
		if err := yaml.Unmarshal(body, rawCfg); err != nil {
			sourceLabel := configPath
			if isHTTPURL(configPath) {
				sourceLabel = "[remote config]"
			}
			return nil, fmt.Errorf("unable to parse config at path %s: %w", sourceLabel, err)
		}
		if err := rejectUnsupportedDialerProxyConfigs(
			fmt.Sprintf("source %d top-level", sourceOrder), rawCfg.Proxies); err != nil {
			return nil, err
		}
		sourceProxies := make([]loadedProxy, 0, len(rawCfg.Proxies))
		proxiesConfig := rawCfg.Proxies
		providersConfig := rawCfg.Providers
		topLevelNames := make(map[string]struct{}, len(proxiesConfig))

		for i, config := range proxiesConfig {
			originalConfig := cloneProxyConfig(config)
			proxy, err := adapter.ParseProxy(config)
			if err != nil {
				return nil, fmt.Errorf("proxy %d: %w", i, err)
			}

			if _, exist := topLevelNames[proxy.Name()]; exist {
				return nil, fmt.Errorf("proxy %s is the duplicate name", proxy.Name())
			}
			topLevelNames[proxy.Name()] = struct{}{}
			sourceProxies = append(sourceProxies, loadedProxy{
				name:             proxy.Name(),
				proxy:            &CProxy{Proxy: proxy, Config: config},
				sourceOrder:      sourceOrder,
				sourceOrigin:     source.Origin,
				sourceIdentifier: source.Path,
				originalName:     proxy.Name(),
				originalConfig:   originalConfig,
			})
		}
		providerNames := make([]string, 0, len(providersConfig))
		for name := range providersConfig {
			providerNames = append(providerNames, name)
		}
		sort.Strings(providerNames)
		for _, name := range providerNames {
			config := providersConfig[name]
			if name == provider.ReservedName {
				return nil, fmt.Errorf("can not defined a provider called `%s`", provider.ReservedName)
			}
			providerProxyConfigs, _, err := st.loadProviderProxyConfigsFromSource(source, name, config)
			if err != nil {
				return nil, err
			}
			preparedProviderConfigs, err := prepareProviderProxyConfigEntries(providerProxyConfigs, config)
			if err != nil {
				return nil, fmt.Errorf("prepare proxy provider %s configs: %w", name, err)
			}
			mappedProviderProxyConfigs := make([]map[string]any, len(preparedProviderConfigs))
			providerParserPayload := make([]map[string]any, len(preparedProviderConfigs))
			for index, preparedConfig := range preparedProviderConfigs {
				mappedProviderProxyConfigs[index] = preparedConfig.config
				parserConfig := cloneProxyConfig(preparedConfig.config)
				parserConfig["name"] = fmt.Sprintf("__clash_speedtest_provider_item_%d__", index+1)
				providerParserPayload[index] = parserConfig
			}

			// Convert the already-fetched response into an inline provider. This
			// keeps Mihomo's proxy construction and Provider validation without
			// issuing a second HTTP request. Filtering and overrides were already
			// applied above, so remove them here and give every record an internal
			// index-based name; Mihomo otherwise drops raw duplicate names.
			inlineConfig := cloneProxyConfig(config)
			inlineConfig["type"] = "inline"
			inlineConfig["payload"] = providerParserPayload
			delete(inlineConfig, "url")
			delete(inlineConfig, "path")
			for _, field := range []string{
				"filter", "exclude-filter", "exclude-type", "dialer-proxy", "override", "age-secret-key",
			} {
				delete(inlineConfig, field)
			}
			pd, err := provider.ParseProxyProvider(name, inlineConfig, nil)
			if err != nil {
				return nil, fmt.Errorf("parse proxy provider %s error: %w", name, err)
			}
			if closer, ok := pd.(interface{ Close() error }); ok {
				defer closer.Close()
			}
			if err := rejectUnsupportedDialerProxyConfigs(
				fmt.Sprintf("proxy provider %s", name), mappedProviderProxyConfigs); err != nil {
				return nil, err
			}
			providerProxies := pd.Proxies()
			if len(providerProxies) != len(mappedProviderProxyConfigs) {
				return nil, fmt.Errorf("proxy provider %s returned %d proxies but mapped %d configs",
					name, len(providerProxies), len(mappedProviderProxyConfigs))
			}
			for index, proxy := range providerProxies {
				preparedConfig := preparedProviderConfigs[index]
				proxyConfig := preparedConfig.config
				configName, ok := stringMapValue(proxyConfig, "name")
				expectedParserName := fmt.Sprintf("__clash_speedtest_provider_item_%d__", index+1)
				if !ok || proxy.Name() != expectedParserName {
					return nil, fmt.Errorf("proxy provider %s proxy/config name mismatch at index %d", name, index)
				}
				sourceProxies = append(sourceProxies, loadedProxy{
					name:             fmt.Sprintf("[%s] %s", name, configName),
					proxy:            &CProxy{Proxy: proxy, Config: proxyConfig},
					sourceOrder:      sourceOrder,
					sourceOrigin:     source.Origin,
					sourceIdentifier: source.Path,
					providerName:     name,
					originalName:     preparedConfig.originalName,
					originalConfig:   preparedConfig.originalConfig,
				})
			}
		}
		for _, item := range sourceProxies {
			p := item.proxy
			if p == nil || p.Config == nil {
				continue
			}
			switch p.Type() {
			case constant.Shadowsocks, constant.ShadowsocksR, constant.Snell, constant.Socks5, constant.Http,
				constant.Vmess, constant.Vless, constant.Trojan, constant.Hysteria, constant.Hysteria2,
				constant.WireGuard, constant.Tuic, constant.Ssh, constant.Mieru, constant.AnyTLS, constant.Sudoku:
			default:
				continue
			}
			if server, ok := stringMapValue(p.Config, "server"); ok {
				p.Config["server"] = convertMappedIPv6ToIPv4(server)
			}
			loadedProxies = append(loadedProxies, item)
		}
	}

	filterRegexp := regexp.MustCompile(st.config.FilterRegex)
	var blockKeywords []string
	if st.config.BlockRegex != "" {
		for _, keyword := range strings.Split(st.config.BlockRegex, "|") {
			keyword = strings.TrimSpace(keyword)
			if keyword != "" {
				blockKeywords = append(blockKeywords, strings.ToLower(keyword))
			}
		}
	}

	selectedProxies := make([]loadedProxy, 0, len(loadedProxies))
	for _, item := range loadedProxies {
		name := item.name
		shouldBlock := false
		if len(blockKeywords) > 0 {
			lowerName := strings.ToLower(name)
			for _, keyword := range blockKeywords {
				if strings.Contains(lowerName, keyword) {
					shouldBlock = true
					break
				}
			}
		}

		if shouldBlock {
			continue
		}
		if filterRegexp.MatchString(name) {
			selectedProxies = append(selectedProxies, item)
		}
	}
	return assignUniqueProxyNames(selectedProxies)
}

func (st *SpeedTester) loadProviderProxyConfigsFromSource(
	source ConfigSource,
	name string,
	config map[string]any,
) ([]map[string]any, string, error) {
	definition, err := parseProviderSourceDefinition(name, config)
	if err != nil {
		return nil, "", err
	}

	switch definition.typeName {
	case "http":
		var requestDialer constant.Dialer
		if source.Origin != SourceOriginLocal {
			requestDialer = st.remoteProviderDialer
			if requestDialer == nil {
				requestDialer = newRestrictedProviderDialer()
			}
		}
		body, fetchErr := st.fetchHTTPConfigWithOptions(
			definition.url, definition.headers, definition.sizeLimit, requestDialer, true)
		if fetchErr != nil {
			var sizeErr *httpConfigSizeError
			if errors.As(fetchErr, &sizeErr) {
				return nil, "", fmt.Errorf(
					"proxy provider %s response exceeds configured size-limit %d", name, sizeErr.limit)
			}
			var statusErr *httpConfigStatusError
			if errors.As(fetchErr, &statusErr) {
				return nil, "", fmt.Errorf(
					"proxy provider %s server returned %d %s",
					name, statusErr.statusCode, http.StatusText(statusErr.statusCode))
			}
			var blockedErr *providerNetworkBlockedError
			if errors.As(fetchErr, &blockedErr) {
				return nil, "", fmt.Errorf(
					"proxy provider %s request blocked: %s", name, blockedErr)
			}
			var redirectErr *providerRedirectBlockedError
			if errors.As(fetchErr, &redirectErr) {
				return nil, "", fmt.Errorf(
					"proxy provider %s redirect blocked: %s", name, redirectErr)
			}
			return nil, "", fmt.Errorf("failed to fetch proxy provider %s", name)
		}
		providerConfigs, parseErr := parseProviderProxyConfigs(name, body)
		return validateProviderDialerProxyConfigs(name, config, providerConfigs, "", parseErr)

	case "file":
		if source.Origin != SourceOriginLocal {
			return nil, "", fmt.Errorf(
				"proxy provider %s type file is forbidden for %s sources", name, source.Origin)
		}
		basePath := source.LocalDependency
		if basePath == "" {
			basePath = source.Path
		}
		providerPath := definition.path
		if !filepath.IsAbs(providerPath) {
			providerPath = filepath.Join(filepath.Dir(basePath), providerPath)
		}
		providerPath, err = filepath.Abs(providerPath)
		if err != nil {
			return nil, "", fmt.Errorf("resolve proxy provider %s path: %w", name, err)
		}
		providerPath = filepath.Clean(providerPath)
		body, readErr := os.ReadFile(providerPath)
		if readErr != nil {
			return nil, "", fmt.Errorf("failed to read proxy provider %s: %w", name, readErr)
		}
		providerConfigs, parseErr := parseProviderProxyConfigs(name, body)
		return validateProviderDialerProxyConfigs(name, config, providerConfigs, providerPath, parseErr)

	case "inline":
		providerConfigs, payloadErr := providerPayloadProxyConfigs(definition.payload)
		if payloadErr != nil {
			return nil, "", fmt.Errorf("unable to parse inline proxy provider %s: %w", name, payloadErr)
		}
		return validateProviderDialerProxyConfigs(name, config, providerConfigs, "", nil)
	default:
		return nil, "", fmt.Errorf("proxy provider %s has unsupported type %q", name, definition.typeName)
	}
}

func parseProviderSourceDefinition(
	name string,
	config map[string]any,
) (*providerSourceDefinition, error) {
	if config == nil {
		return nil, fmt.Errorf("proxy provider %s config is empty", name)
	}
	typeName, err := requiredProviderString(config, "type")
	if err != nil {
		return nil, fmt.Errorf("proxy provider %s: %w", name, err)
	}
	if typeName != strings.ToLower(typeName) {
		return nil, fmt.Errorf("proxy provider %s type must be lowercase", name)
	}

	definition := &providerSourceDefinition{typeName: typeName, sizeLimit: maxHTTPConfigSize}
	switch typeName {
	case "http":
		if err := rejectProviderFields(name, config, "path", "payload"); err != nil {
			return nil, err
		}
		providerURL, valueErr := requiredProviderString(config, "url")
		if valueErr != nil {
			return nil, fmt.Errorf("proxy provider %s: %w", name, valueErr)
		}
		parsedURL, parseErr := url.Parse(providerURL)
		if parseErr != nil || parsedURL.Host == "" ||
			(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return nil, fmt.Errorf("proxy provider %s url must be an absolute http or https URL", name)
		}
		definition.url = providerURL

		if _, exists := config["proxy"]; exists {
			return nil, fmt.Errorf("proxy provider %s field proxy is not supported; request was not sent", name)
		}
		if _, exists := config["age-secret-key"]; exists {
			return nil, fmt.Errorf(
				"proxy provider %s field age-secret-key is not supported; request was not sent", name)
		}
		if rawHeader, exists := config["header"]; exists {
			headers, headerErr := parseProviderHeaders(rawHeader)
			if headerErr != nil {
				return nil, fmt.Errorf("proxy provider %s header: %w", name, headerErr)
			}
			definition.headers = headers
		}
		if rawSizeLimit, exists := config["size-limit"]; exists {
			sizeLimit, sizeErr := strictPositiveInt64(rawSizeLimit)
			if sizeErr != nil {
				return nil, fmt.Errorf("proxy provider %s size-limit: %w", name, sizeErr)
			}
			if sizeLimit > maxHTTPConfigSize {
				return nil, fmt.Errorf(
					"proxy provider %s size-limit exceeds supported maximum %d", name, maxHTTPConfigSize)
			}
			definition.sizeLimit = sizeLimit
		}

	case "file":
		if err := rejectProviderFields(
			name, config, "url", "payload", "header", "proxy", "size-limit", "age-secret-key"); err != nil {
			return nil, err
		}
		providerPath, valueErr := requiredProviderString(config, "path")
		if valueErr != nil {
			return nil, fmt.Errorf("proxy provider %s: %w", name, valueErr)
		}
		definition.path = providerPath

	case "inline":
		if err := rejectProviderFields(
			name, config, "url", "path", "header", "proxy", "size-limit", "age-secret-key"); err != nil {
			return nil, err
		}
		payload, exists := config["payload"]
		if !exists {
			return nil, fmt.Errorf("proxy provider %s field payload is required for type inline", name)
		}
		definition.payload = payload

	default:
		return nil, fmt.Errorf("proxy provider %s has unsupported type %q", name, typeName)
	}
	return definition, nil
}

func requiredProviderString(config map[string]any, field string) (string, error) {
	raw, exists := config[field]
	if !exists {
		return "", fmt.Errorf("field %s is required", field)
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("field %s must be a non-empty string", field)
	}
	return strings.TrimSpace(value), nil
}

func rejectProviderFields(name string, config map[string]any, fields ...string) error {
	for _, field := range fields {
		if _, exists := config[field]; exists {
			return fmt.Errorf("proxy provider %s field %s conflicts with type %s", name, field, config["type"])
		}
	}
	return nil
}

func parseProviderHeaders(raw any) (http.Header, error) {
	mapping, ok := stringAnyMap(raw)
	if !ok {
		return nil, fmt.Errorf("must be a mapping of header names to string lists")
	}
	headers := make(http.Header, len(mapping))
	for rawName, rawValues := range mapping {
		trimmedName := strings.TrimSpace(rawName)
		if strings.EqualFold(trimmedName, "Host") {
			return nil, fmt.Errorf("header Host is not supported; request was not sent")
		}
		if !isHTTPHeaderName(trimmedName) {
			return nil, fmt.Errorf("contains an invalid header name")
		}
		name := textproto.CanonicalMIMEHeaderKey(trimmedName)
		values := make([]string, 0)
		switch typed := rawValues.(type) {
		case string:
			values = append(values, typed)
		case []string:
			values = append(values, typed...)
		case []any:
			for _, rawValue := range typed {
				value, valueOK := rawValue.(string)
				if !valueOK {
					return nil, fmt.Errorf("header %s contains a non-string value", name)
				}
				values = append(values, value)
			}
		default:
			return nil, fmt.Errorf("header %s must be a string or string list", name)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("header %s contains no values", name)
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return nil, fmt.Errorf("header %s contains a line break", name)
			}
			headers.Add(name, value)
		}
	}
	return headers, nil
}

func isHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func strictPositiveInt64(raw any) (int64, error) {
	var value int64
	switch typed := raw.(type) {
	case int:
		value = int64(typed)
	case int32:
		value = int64(typed)
	case int64:
		value = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, fmt.Errorf("must fit in int64")
		}
		value = int64(typed)
	case uint32:
		value = int64(typed)
	case uint64:
		if typed > math.MaxInt64 {
			return 0, fmt.Errorf("must fit in int64")
		}
		value = int64(typed)
	default:
		return 0, fmt.Errorf("must be an integer")
	}
	if value <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return value, nil
}

func parseProviderProxyConfigs(name string, body []byte) ([]map[string]any, error) {
	rawConfig := &RawConfig{}
	yamlErr := yaml.Unmarshal(body, rawConfig)
	if yamlErr == nil {
		if len(rawConfig.Proxies) > 0 {
			return rawConfig.Proxies, nil
		}
		if len(rawConfig.ProviderPayload) > 0 {
			return rawConfig.ProviderPayload, nil
		}
		return nil, fmt.Errorf("proxy provider %s contains no proxies", name)
	}
	return convertStrictProviderURIs(name, body)
}

func convertStrictProviderURIs(name string, body []byte) ([]map[string]any, error) {
	decoded := convert.DecodeBase64(body)
	nonEmptyLines := 0
	for index, rawLine := range strings.Split(string(decoded), "\n") {
		line := strings.TrimRight(rawLine, " \r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmptyLines++
		converted, err := convert.ConvertsV2Ray([]byte(line))
		if err != nil || len(converted) != 1 {
			return nil, fmt.Errorf(
				"proxy provider %s URI line %d is invalid or unsupported", name, index+1)
		}
	}
	if nonEmptyLines == 0 {
		return nil, fmt.Errorf("proxy provider %s contains no proxies", name)
	}

	converted, err := convert.ConvertsV2Ray(decoded)
	if err != nil || len(converted) != nonEmptyLines {
		return nil, fmt.Errorf("proxy provider %s URI list could not be converted completely", name)
	}
	return converted, nil
}

func providerPayloadProxyConfigs(payload any) ([]map[string]any, error) {
	items, ok := anySlice(payload)
	if !ok {
		return nil, fmt.Errorf("payload must be a list")
	}
	configs := make([]map[string]any, 0, len(items))
	for index, item := range items {
		config, ok := stringAnyMap(item)
		if !ok {
			return nil, fmt.Errorf("payload item %d must be a mapping", index)
		}
		configs = append(configs, config)
	}
	if len(configs) == 0 {
		return nil, fmt.Errorf("payload contains no proxies")
	}
	return configs, nil
}

func assignUniqueProxyNames(proxies []loadedProxy) (map[string]*CProxy, error) {
	reservedNames := make(map[string]struct{}, len(proxies))
	for _, item := range proxies {
		if strings.TrimSpace(item.name) == "" || item.proxy == nil || item.proxy.Config == nil {
			return nil, fmt.Errorf("loaded proxy from source %d has invalid naming data", item.sourceOrder)
		}
		reservedNames[item.name] = struct{}{}
	}

	result := make(map[string]*CProxy, len(proxies))
	usedNames := make(map[string]struct{}, len(proxies))
	for _, item := range proxies {
		name := item.name
		if _, used := usedNames[name]; used {
			for index := 2; ; index++ {
				candidate := fmt.Sprintf("%s [%d]", item.name, index)
				_, used = usedNames[candidate]
				_, reserved := reservedNames[candidate]
				if !used && !reserved {
					name = candidate
					break
				}
			}
		}

		config := cloneProxyConfig(item.proxy.Config)
		config["name"] = name
		item.proxy.Config = config
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("unique proxy name %q still collides", name)
		}
		result[name] = item.proxy
		usedNames[name] = struct{}{}
	}
	return result, nil
}

func validateProviderDialerProxyConfigs(
	providerName string,
	providerConfig map[string]any,
	proxyConfigs []map[string]any,
	dependency string,
	loadErr error,
) ([]map[string]any, string, error) {
	if loadErr != nil {
		return nil, "", loadErr
	}
	if err := rejectUnsupportedDialerProxyConfigs(
		fmt.Sprintf("proxy provider %s", providerName), proxyConfigs); err != nil {
		return nil, "", err
	}

	nodeName := "unknown node"
	if len(proxyConfigs) > 0 {
		if name, ok := stringMapValue(proxyConfigs[0], "name"); ok {
			nodeName = name
		}
	}
	if hasNonEmptyDialerProxy(providerConfig) {
		return nil, "", fmt.Errorf(
			"proxy provider %s node %q receives dialer-proxy from provider settings, which is not supported",
			providerName, nodeName)
	}
	if rawOverride, exists := providerConfig["override"]; exists {
		if override, ok := stringAnyMap(rawOverride); ok && hasNonEmptyDialerProxy(override) {
			return nil, "", fmt.Errorf(
				"proxy provider %s node %q receives dialer-proxy from provider override, which is not supported",
				providerName, nodeName)
		}
	}
	return proxyConfigs, dependency, nil
}

func rejectUnsupportedDialerProxyConfigs(scope string, proxyConfigs []map[string]any) error {
	for index, proxyConfig := range proxyConfigs {
		if !hasNonEmptyDialerProxy(proxyConfig) {
			continue
		}
		name := fmt.Sprintf("#%d", index+1)
		if configuredName, ok := stringMapValue(proxyConfig, "name"); ok {
			name = configuredName
		}
		return fmt.Errorf("%s node %q uses dialer-proxy, which is not supported", scope, name)
	}
	return nil
}

func hasNonEmptyDialerProxy(config map[string]any) bool {
	raw, exists := config["dialer-proxy"]
	if !exists || raw == nil {
		return false
	}
	value, ok := raw.(string)
	if !ok {
		return true
	}
	return strings.TrimSpace(value) != ""
}

func cloneProxyConfig(config map[string]any) map[string]any {
	cloned := make(map[string]any, len(config))
	for key, value := range config {
		cloned[key] = value
	}
	return cloned
}

func prepareProviderProxyConfigs(proxies []map[string]any, providerConfig map[string]any) ([]map[string]any, error) {
	entries, err := prepareProviderProxyConfigEntries(proxies, providerConfig)
	if err != nil {
		return nil, err
	}
	prepared := make([]map[string]any, len(entries))
	for index, entry := range entries {
		prepared[index] = entry.config
	}
	return prepared, nil
}

func prepareProviderProxyConfigEntries(
	proxies []map[string]any,
	providerConfig map[string]any,
) ([]preparedProviderProxyConfig, error) {
	filter, _ := stringMapValue(providerConfig, "filter")
	filterRegexps, err := compileProviderRegexps(filter, true)
	if err != nil {
		return nil, fmt.Errorf("invalid filter regex: %w", err)
	}
	excludeFilter, _ := stringMapValue(providerConfig, "exclude-filter")
	excludeRegexps, err := compileProviderRegexps(excludeFilter, false)
	if err != nil {
		return nil, fmt.Errorf("invalid exclude-filter regex: %w", err)
	}
	excludeType, _ := stringMapValue(providerConfig, "exclude-type")
	var excludedTypes []string
	if excludeType != "" {
		excludedTypes = strings.Split(excludeType, "|")
	}

	selected := make([]preparedProviderProxyConfig, 0, len(proxies))
	selectedIndexes := make(map[int]struct{}, len(proxies))
	for _, filterRegexp := range filterRegexps {
		for proxyIndex, proxyConfig := range proxies {
			proxyName, ok := stringMapValue(proxyConfig, "name")
			if !ok {
				continue
			}
			if _, exists := selectedIndexes[proxyIndex]; exists {
				continue
			}
			excluded, excludeErr := providerProxyExcluded(
				proxyConfig, proxyName, excludedTypes, excludeRegexps)
			if excludeErr != nil {
				return nil, excludeErr
			}
			if excluded {
				continue
			}
			if filter != "" {
				matches, matchErr := filterRegexp.MatchString(proxyName)
				if matchErr != nil {
					return nil, providerRegexRuntimeError("Provider filter", matchErr)
				}
				if !matches {
					continue
				}
			}

			prepared := cloneProxyConfig(proxyConfig)
			if dialerProxy, ok := stringMapValue(providerConfig, "dialer-proxy"); ok && dialerProxy != "" {
				prepared["dialer-proxy"] = dialerProxy
			}
			if err := applyProviderOverrides(prepared, providerConfig["override"]); err != nil {
				return nil, err
			}
			selected = append(selected, preparedProviderProxyConfig{
				originalName:   proxyName,
				originalConfig: cloneProxyConfig(proxyConfig),
				config:         prepared,
			})
			selectedIndexes[proxyIndex] = struct{}{}
		}
	}
	return selected, nil
}

func compileProviderRegexps(value string, includeEmpty bool) ([]*regexp2.Regexp, error) {
	if value == "" && !includeEmpty {
		return nil, nil
	}
	parts := strings.Split(value, "`")
	regexps := make([]*regexp2.Regexp, 0, len(parts))
	for _, part := range parts {
		compiled, err := compileProviderRegexp(part)
		if err != nil {
			return nil, err
		}
		regexps = append(regexps, compiled)
	}
	return regexps, nil
}

func providerProxyExcluded(
	proxyConfig map[string]any,
	proxyName string,
	excludedTypes []string,
	excludeRegexps []*regexp2.Regexp,
) (bool, error) {
	if len(excludedTypes) > 0 {
		proxyType, ok := stringMapValue(proxyConfig, "type")
		if !ok {
			return true, nil
		}
		for _, excludedType := range excludedTypes {
			if strings.EqualFold(proxyType, excludedType) {
				return true, nil
			}
		}
	}
	for _, excludeRegexp := range excludeRegexps {
		matches, err := excludeRegexp.MatchString(proxyName)
		if err != nil {
			return false, providerRegexRuntimeError("Provider exclude-filter", err)
		}
		if matches {
			return true, nil
		}
	}
	return false, nil
}

func compileProviderRegexp(pattern string) (*regexp2.Regexp, error) {
	compiled, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return nil, err
	}
	compiled.MatchTimeout = providerRegexMatchTimeout
	return compiled, nil
}

func providerRegexRuntimeError(operation string, err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "match timeout") {
		return fmt.Errorf("%s exceeded the %s match timeout", operation, providerRegexMatchTimeout)
	}
	return fmt.Errorf("%s failed", operation)
}

func applyProviderOverrides(proxyConfig map[string]any, rawOverride any) error {
	override, ok := stringAnyMap(rawOverride)
	if !ok {
		return nil
	}
	for _, key := range []string{
		"tfo", "mptcp", "udp", "udp-over-tcp", "up", "down", "dialer-proxy",
		"skip-cert-verify", "interface-name", "routing-mark", "ip-version",
	} {
		if value, exists := override[key]; exists {
			proxyConfig[key] = value
		}
	}

	name, ok := stringMapValue(proxyConfig, "name")
	if !ok {
		return nil
	}
	if replacements, ok := anySlice(override["proxy-name"]); ok {
		for _, rawReplacement := range replacements {
			replacement, ok := stringAnyMap(rawReplacement)
			if !ok {
				continue
			}
			pattern, patternOK := stringMapValue(replacement, "pattern")
			target, targetOK := stringMapValue(replacement, "target")
			if !patternOK || !targetOK {
				continue
			}
			compiled, err := compileProviderRegexp(pattern)
			if err != nil {
				return fmt.Errorf("invalid proxy-name override regex: %w", err)
			}
			name, err = compiled.Replace(name, target, 0, -1)
			if err != nil {
				return providerRegexRuntimeError("Provider proxy-name override", err)
			}
		}
	}
	if prefix, ok := stringMapValue(override, "additional-prefix"); ok {
		name = prefix + name
	}
	if suffix, ok := stringMapValue(override, "additional-suffix"); ok {
		name += suffix
	}
	proxyConfig["name"] = name
	return nil
}

func stringAnyMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return nil, false
			}
			converted[stringKey] = item
		}
		return converted, true
	default:
		return nil, false
	}
}

func anySlice(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []map[string]any:
		items := make([]any, len(typed))
		for index := range typed {
			items[index] = typed[index]
		}
		return items, true
	default:
		return nil, false
	}
}

func isHTTPURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func stringMapValue(values map[string]any, key string) (string, bool) {
	if values == nil {
		return "", false
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return "", false
	}
	value, ok := raw.(string)
	return value, ok
}

func (st *SpeedTester) TestProxies(proxies map[string]*CProxy, tester func(result *Result)) {
	names := make([]string, 0, len(proxies))
	for name := range proxies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tester(st.testProxy(name, proxies[name]))
	}
}

type Result struct {
	ProxyName               string         `json:"proxy_name"`
	ProxyType               string         `json:"proxy_type"`
	ProxyConfig             map[string]any `json:"proxy_config"`
	Latency                 time.Duration  `json:"latency"`
	Jitter                  time.Duration  `json:"jitter"`
	HTTPProbeFailurePercent float64        `json:"http_probe_failure_percent"`
	DownloadSize            float64        `json:"download_size"`
	DownloadTime            time.Duration  `json:"download_time"`
	DownloadSpeed           float64        `json:"download_speed"`
	DownloadError           string         `json:"download_error"`
	DownloadTested          bool           `json:"download_tested"`
	DownloadComplete        bool           `json:"download_complete"`
}

func (r *Result) FormatDownloadSpeed() string {
	if r.DownloadSpeed <= 0 && r.DownloadError != "" {
		return r.DownloadError
	}
	return formatSpeed(r.DownloadSpeed)
}

func (r *Result) FormatDownloadSpeedValue() string {
	return formatSpeed(r.DownloadSpeed)
}

func (r *Result) FormatLatency() string {
	if r.Latency == 0 {
		return "N/A"
	}
	return fmt.Sprintf("%dms", r.Latency.Milliseconds())
}

func (r *Result) FormatJitter() string {
	if r.Jitter == 0 {
		return "N/A"
	}
	return fmt.Sprintf("%dms", r.Jitter.Milliseconds())
}

func (r *Result) FormatHTTPProbeFailure() string {
	return fmt.Sprintf("%.1f%%", r.HTTPProbeFailurePercent)
}

func (r *Result) FormatDownloadError() string {
	if r.DownloadError == "" {
		return "N/A"
	}
	return r.DownloadError
}

func formatSpeed(bytesPerSecond float64) string {
	if bytesPerSecond == 0 {
		return "N/A"
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"}
	unit := 0
	speed := bytesPerSecond
	for speed >= 1024 && unit < len(units)-1 {
		speed /= 1024
		unit++
	}
	return fmt.Sprintf("%.2f%s", speed, units[unit])
}

func (st *SpeedTester) testProxy(name string, proxy *CProxy) *Result {
	result := st.ProbeProxy(name, proxy)
	if st.ShouldTestTransfers(result) {
		st.TestTransfers(result, proxy)
	}
	return result
}

// ProbeProxy performs the low-bandwidth HTTP probe phase for a single node.
// It is safe to run this method concurrently for different proxies.
func (st *SpeedTester) ProbeProxy(name string, proxy *CProxy) *Result {
	result := &Result{
		ProxyName:   name,
		ProxyType:   proxy.Type().String(),
		ProxyConfig: proxy.Config,
	}

	latencyResult := st.testLatency(proxy, st.latencyRequestTimeout())
	result.Latency = latencyResult.latency
	result.Jitter = latencyResult.jitter
	result.HTTPProbeFailurePercent = latencyResult.httpProbeFailurePercent
	return result
}

// ShouldTestTransfers determines whether a probed node should enter the
// bandwidth-intensive phase. GUI runs always have an output path and apply
// configured thresholds before consuming transfer traffic; CLI display-only
// runs retain the historical behavior of testing every reachable node.
func (st *SpeedTester) ShouldTestTransfers(result *Result) bool {
	if result == nil || st.mode.IsFast() || result.Latency <= 0 || result.HTTPProbeFailurePercent >= 100 {
		return false
	}
	if st.config.OutputPath != "" && st.config.MaxHTTPProbeFailure < 100 && result.HTTPProbeFailurePercent > st.config.MaxHTTPProbeFailure {
		return false
	}
	if st.config.OutputPath != "" && st.config.MaxLatency > 0 && result.Latency > st.config.MaxLatency {
		return false
	}
	return true
}

// TestTransfers performs the bandwidth-intensive phase for a previously
// probed node. The caller serializes calls across nodes so each node gets the
// machine's available bandwidth; connections within one node remain parallel.
func (st *SpeedTester) TestTransfers(result *Result, proxy *CProxy) {
	if result == nil || proxy == nil || st.mode.IsFast() {
		return
	}
	downloadSummary := newTransferSummary()

	downloadChunks := splitTransferSizes(st.config.DownloadSize, st.config.Concurrent)
	if len(downloadChunks) > 0 {
		result.DownloadTested = true
		downloadResults := make(chan *downloadResult, len(downloadChunks))
		batchStarted := time.Now()
		var wg sync.WaitGroup
		for _, chunkSize := range downloadChunks {
			wg.Add(1)
			go func(size int) {
				defer wg.Done()
				downloadResults <- st.testDownload(proxy, size, st.config.DownloadTimeout)
			}(chunkSize)
		}
		wg.Wait()
		batchDuration := time.Since(batchStarted)
		for range downloadChunks {
			if dr := <-downloadResults; dr != nil {
				downloadSummary.add(dr)
			}
		}
		close(downloadResults)
		result.DownloadSize, result.DownloadTime, result.DownloadSpeed, result.DownloadError, result.DownloadComplete =
			applyTransferSummary(downloadSummary, batchDuration, len(downloadChunks))

	}
}

func splitTransferSizes(total, parallel int) []int {
	if total <= 0 || parallel <= 0 {
		return nil
	}
	if parallel > total {
		parallel = total
	}
	chunks := make([]int, parallel)
	base := total / parallel
	remainder := total % parallel
	for i := range chunks {
		chunks[i] = base
		if i < remainder {
			chunks[i]++
		}
	}
	return chunks
}

func (st *SpeedTester) latencyRequestTimeout() time.Duration {
	return st.config.ProbeTimeout
}

type latencyResult struct {
	latency                 time.Duration
	jitter                  time.Duration
	httpProbeFailurePercent float64
}

func (st *SpeedTester) testLatency(proxy constant.Proxy, requestTimeout time.Duration) *latencyResult {
	client := st.createClient(proxy, requestTimeout)
	defer client.CloseIdleConnections()

	for range latencyWarmupRequests {
		_ = st.probeLatencyRequest(client)
	}

	latencies := make([]time.Duration, 0, latencyMeasuredRequests)
	failedPings := 0
	for index := range latencyMeasuredRequests {
		if index > 0 {
			time.Sleep(latencyProbeInterval)
		}
		start := time.Now()
		if err := st.probeLatencyRequest(client); err != nil {
			failedPings++
			continue
		}
		latencies = append(latencies, time.Since(start))
	}
	return calculateLatencyStats(latencies, failedPings, latencyMeasuredRequests)
}

func (st *SpeedTester) latencyURL() string {
	if st.serverMode == serverModeDownloadServer {
		return fmt.Sprintf("%s/__down?bytes=1", st.serverBaseURL)
	}
	return st.downloadURL
}

func (st *SpeedTester) probeLatencyRequest(client *http.Client) error {
	requestURL := st.latencyURL()
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	if st.serverMode == serverModeDirectDownload {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("probe returned %s", resp.Status)
	}
	readBytes, err := io.CopyN(io.Discard, resp.Body, 1)
	if err != nil {
		return fmt.Errorf("probe response is shorter than one byte: %w", err)
	}
	if readBytes != 1 {
		return fmt.Errorf("probe response returned %d bytes", readBytes)
	}
	return nil
}

type downloadResult struct {
	error    string
	bytes    int64
	duration time.Duration
}

type transferSummary struct {
	totalBytes   int64
	successCount int
	errors       []string
	errorSeen    map[string]struct{}
}

func applyTransferSummary(
	summary *transferSummary,
	batchDuration time.Duration,
	expectedCount int,
) (float64, time.Duration, float64, string, bool) {
	if summary == nil {
		return 0, 0, 0, "", false
	}
	var size float64
	var duration time.Duration
	var speed float64
	var errorMessage string
	if summary.totalBytes > 0 {
		size = float64(summary.totalBytes)
		duration = batchDuration
		if duration > 0 {
			speed = float64(summary.totalBytes) / duration.Seconds()
		}
	}
	if len(summary.errors) > 0 {
		errorMessage = strings.Join(summary.errors, "; ")
	}
	complete := expectedCount > 0 && summary.successCount == expectedCount && len(summary.errors) == 0
	return size, duration, speed, errorMessage, complete
}

func newTransferSummary() *transferSummary {
	return &transferSummary{
		errorSeen: make(map[string]struct{}),
	}
}

func (s *transferSummary) add(result *downloadResult) {
	if result == nil {
		return
	}
	s.totalBytes += result.bytes
	if result.error != "" {
		s.appendError(result.error)
		return
	}
	s.successCount++
}

func (s *transferSummary) appendError(message string) {
	if message == "" {
		return
	}
	if s.errorSeen == nil {
		s.errorSeen = make(map[string]struct{})
	}
	if _, exists := s.errorSeen[message]; exists {
		return
	}
	s.errorSeen[message] = struct{}{}
	s.errors = append(s.errors, message)
}

func (st *SpeedTester) testDownload(proxy constant.Proxy, size int, timeout time.Duration) *downloadResult {
	client := st.createClient(proxy, timeout)
	defer client.CloseIdleConnections()

	start := time.Now()
	var downloadURL string
	if st.serverMode == serverModeDirectDownload {
		downloadURL = st.downloadURL
	} else {
		downloadURL = fmt.Sprintf("%s/__down?bytes=%d", st.serverBaseURL, size)
	}

	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return &downloadResult{
			error: fmt.Sprintf("create download request for %s failed: %v", downloadURL, err),
		}
	}
	if st.serverMode == serverModeDirectDownload && size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", size-1))
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return &downloadResult{
			error: fmt.Sprintf("download request to %s failed: %v, spent %s", downloadURL, err, time.Since(start)),
		}
	}
	defer resp.Body.Close()

	downloadBytes, readErr := consumeDownloadResponse(resp, size)
	if readErr != nil {
		return &downloadResult{
			bytes:    downloadBytes,
			duration: time.Since(start),
			error:    fmt.Sprintf("download response from %s is invalid: %v, spent %s", downloadURL, readErr, time.Since(start)),
		}
	}
	return &downloadResult{
		bytes:    downloadBytes,
		duration: time.Since(start),
	}
}

var contentRangePattern = regexp.MustCompile(`^bytes\s+(\d+)-(\d+)/(?:\d+|\*)$`)

func consumeDownloadResponse(resp *http.Response, size int) (int64, error) {
	if resp == nil || resp.Body == nil {
		return 0, fmt.Errorf("response body is empty")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("returned %s", resp.Status)
	}
	if size <= 0 {
		return 0, fmt.Errorf("requested size must be positive")
	}
	if resp.StatusCode == http.StatusPartialContent {
		matches := contentRangePattern.FindStringSubmatch(strings.TrimSpace(resp.Header.Get("Content-Range")))
		if len(matches) != 3 {
			return 0, fmt.Errorf("Content-Range is missing or invalid")
		}
		start, startErr := strconv.ParseInt(matches[1], 10, 64)
		end, endErr := strconv.ParseInt(matches[2], 10, 64)
		if startErr != nil || endErr != nil || start != 0 || end != int64(size-1) {
			return 0, fmt.Errorf("Content-Range does not exactly match requested bytes")
		}
	}

	readBytes, err := io.CopyN(io.Discard, resp.Body, int64(size))
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return readBytes, fmt.Errorf("response is shorter than requested: got %d bytes, want %d", readBytes, size)
		}
		return readBytes, err
	}
	return readBytes, nil
}

func (st *SpeedTester) createClient(proxy constant.Proxy, timeout time.Duration) *http.Client {
	return NewProxyHTTPClient(proxy, timeout)
}

// NewProxyHTTPClient creates an HTTP client whose every connection is dialed
// through the selected Mihomo proxy. Callers must never replace its transport,
// otherwise requests would reveal the computer's own exit location.
func NewProxyHTTPClient(proxy constant.Proxy, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				var u16Port uint16
				if port, err := strconv.ParseUint(port, 10, 16); err == nil {
					u16Port = uint16(port)
				}
				return proxy.DialContext(ctx, &constant.Metadata{
					Host:    host,
					DstPort: u16Port,
				})
			},
		},
	}
}

func calculateLatencyStats(latencies []time.Duration, failedPings, totalProbes int) *latencyResult {
	result := &latencyResult{
		httpProbeFailurePercent: 100,
	}
	if totalProbes > 0 {
		result.httpProbeFailurePercent = float64(failedPings) / float64(totalProbes) * 100
	}

	if len(latencies) == 0 {
		return result
	}

	sortedLatencies := append([]time.Duration(nil), latencies...)
	sort.Slice(sortedLatencies, func(i, j int) bool { return sortedLatencies[i] < sortedLatencies[j] })
	middle := len(sortedLatencies) / 2
	if len(sortedLatencies)%2 == 0 {
		result.latency = (sortedLatencies[middle-1] + sortedLatencies[middle]) / 2
	} else {
		result.latency = sortedLatencies[middle]
	}
	// Some Windows clocks can report a zero-duration loopback request. A
	// successful HTTP probe is still reachable, so keep zero reserved for the
	// "no successful probes" state used by the filtering pipeline.
	if result.latency <= 0 {
		result.latency = time.Nanosecond
	}

	var total time.Duration
	for _, latency := range latencies {
		total += latency
	}
	mean := total / time.Duration(len(latencies))
	var variance float64
	for _, l := range latencies {
		diff := float64(l - mean)
		variance += diff * diff
	}
	variance /= float64(len(latencies))
	result.jitter = time.Duration(math.Sqrt(variance))

	return result
}

func convertMappedIPv6ToIPv4(server string) string {
	ip := net.ParseIP(server)
	if ip == nil {
		return server
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return server
}

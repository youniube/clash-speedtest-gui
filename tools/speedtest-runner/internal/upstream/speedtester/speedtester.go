package speedtester

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/constant"
	"gopkg.in/yaml.v2"
)

type Config struct {
	ConfigPaths      string
	FilterRegex      string
	BlockRegex       string
	ServerURL        string
	DownloadSize     int
	UploadSize       int
	Timeout          time.Duration
	Concurrent       int
	MaxLatency       time.Duration
	MaxPacketLoss    float64
	MinDownloadSpeed float64
	MinUploadSpeed   float64
	Mode             SpeedMode
	OutputPath       string
	UserAgent        string // optional; empty means use default (mihomo kernel UA)
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
	timeout := st.config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", st.fetchConfigUA())
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, trimHTTPError(string(detail)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxHTTPConfigSize {
		return nil, fmt.Errorf("remote config exceeds %d bytes", maxHTTPConfigSize)
	}
	return body, nil
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
	config           *Config
	blockedNodes     []string
	blockedNodeCount int
	serverMode       serverMode
	serverBaseURL    string
	downloadURL      string
	mode             SpeedMode
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
	if config.UploadSize < 0 {
		config.UploadSize = 10 * 1024 * 1024
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	mode := config.Mode
	if mode == "" {
		mode = SpeedModeDownload
	}
	target, err := resolveServerTarget(config.ServerURL)
	if err != nil {
		return nil, err
	}
	if mode == SpeedModeFull && config.UploadSize <= 0 {
		return nil, fmt.Errorf("upload size must be positive when speed mode is %s", mode)
	}
	if target.mode == serverModeDirectDownload && mode == SpeedModeFull {
		return nil, fmt.Errorf("direct download URL does not support upload; use a base speed-test server URL for mode %s", mode)
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
	name  string
	proxy *CProxy
}

type RawConfig struct {
	Providers map[string]map[string]any `yaml:"proxy-providers"`
	Proxies   []map[string]any          `yaml:"proxies"`
}

func (st *SpeedTester) LoadProxies() (map[string]*CProxy, error) {
	loadedProxies := make([]loadedProxy, 0)
	st.blockedNodes = make([]string, 0)
	st.blockedNodeCount = 0

	for configPath := range strings.SplitSeq(st.config.ConfigPaths, ",") {
		var body []byte
		var err error
		if isHTTPURL(configPath) {
			body, err = st.fetchHTTPConfig(strings.TrimSpace(configPath))
			if err != nil {
				log.Printf("failed to fetch remote config")
				continue
			}
		} else {
			body, err = os.ReadFile(configPath)
		}
		if err != nil {
			log.Printf("failed to read config: %s", err)
			continue
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
		proxies := make(map[string]*CProxy)
		proxiesConfig := rawCfg.Proxies
		providersConfig := rawCfg.Providers

		for i, config := range proxiesConfig {
			proxy, err := adapter.ParseProxy(config)
			if err != nil {
				return nil, fmt.Errorf("proxy %d: %w", i, err)
			}

			if _, exist := proxies[proxy.Name()]; exist {
				return nil, fmt.Errorf("proxy %s is the duplicate name", proxy.Name())
			}
			proxies[proxy.Name()] = &CProxy{Proxy: proxy, Config: config}
		}
		for name, config := range providersConfig {
			if name == provider.ReservedName {
				return nil, fmt.Errorf("can not defined a provider called `%s`", provider.ReservedName)
			}
			providerURL, ok := stringMapValue(config, "url")
			if !ok || strings.TrimSpace(providerURL) == "" {
				log.Printf("skip proxy provider %s: missing url", name)
				continue
			}
			providerURL = strings.TrimSpace(providerURL)
			body, err = st.fetchHTTPConfig(providerURL)
			if err != nil {
				log.Printf("failed to fetch proxy provider %s", name)
				continue
			}
			pdRawCfg := &RawConfig{
				Proxies: []map[string]any{},
			}
			if err := yaml.Unmarshal(body, pdRawCfg); err != nil {
				return nil, fmt.Errorf("unable to parse proxy provider %s: %w", name, err)
			}

			// Convert the already-fetched response into an inline provider. This
			// preserves Mihomo's filtering and override behavior without issuing a
			// second HTTP request that could return a different node set.
			inlineConfig := cloneProxyConfig(config)
			inlineConfig["type"] = "inline"
			inlineConfig["payload"] = pdRawCfg.Proxies
			delete(inlineConfig, "url")
			delete(inlineConfig, "path")
			pd, err := provider.ParseProxyProvider(name, inlineConfig, nil)
			if err != nil {
				return nil, fmt.Errorf("parse proxy provider %s error: %w", name, err)
			}
			if closer, ok := pd.(interface{ Close() error }); ok {
				defer closer.Close()
			}
			providerProxyConfigs, err := prepareProviderProxyConfigs(pdRawCfg.Proxies, config)
			if err != nil {
				return nil, fmt.Errorf("prepare proxy provider %s configs: %w", name, err)
			}
			providerProxies := pd.Proxies()
			if len(providerProxies) != len(providerProxyConfigs) {
				return nil, fmt.Errorf("proxy provider %s returned %d proxies but mapped %d configs",
					name, len(providerProxies), len(providerProxyConfigs))
			}
			for index, proxy := range providerProxies {
				proxyConfig := providerProxyConfigs[index]
				configName, ok := stringMapValue(proxyConfig, "name")
				if !ok || configName != proxy.Name() {
					return nil, fmt.Errorf("proxy provider %s proxy/config name mismatch at index %d", name, index)
				}
				proxies[fmt.Sprintf("[%s] %s", name, proxy.Name())] = &CProxy{
					Proxy:  proxy,
					Config: proxyConfig,
				}
			}
		}
		names := make([]string, 0, len(proxies))
		for name := range proxies {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			p := proxies[name]
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
			loadedProxies = append(loadedProxies, loadedProxy{name: name, proxy: p})
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
	return assignUniqueProxyNames(selectedProxies), nil
}

func assignUniqueProxyNames(proxies []loadedProxy) map[string]*CProxy {
	reservedNames := make(map[string]struct{}, len(proxies))
	for _, item := range proxies {
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
		result[name] = item.proxy
		usedNames[name] = struct{}{}
	}
	return result
}

func cloneProxyConfig(config map[string]any) map[string]any {
	cloned := make(map[string]any, len(config))
	for key, value := range config {
		cloned[key] = value
	}
	return cloned
}

func prepareProviderProxyConfigs(proxies []map[string]any, providerConfig map[string]any) ([]map[string]any, error) {
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

	selected := make([]map[string]any, 0, len(proxies))
	selectedNames := make(map[string]struct{}, len(proxies))
	for _, filterRegexp := range filterRegexps {
		for _, proxyConfig := range proxies {
			proxyName, ok := stringMapValue(proxyConfig, "name")
			if !ok {
				continue
			}
			if _, exists := selectedNames[proxyName]; exists {
				continue
			}
			if providerProxyExcluded(proxyConfig, proxyName, excludedTypes, excludeRegexps) {
				continue
			}
			if filter != "" {
				matches, matchErr := filterRegexp.MatchString(proxyName)
				if matchErr != nil {
					return nil, fmt.Errorf("match filter against proxy name: %w", matchErr)
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
			selected = append(selected, prepared)
			selectedNames[proxyName] = struct{}{}
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
		compiled, err := regexp2.Compile(part, regexp2.None)
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
) bool {
	if len(excludedTypes) > 0 {
		proxyType, ok := stringMapValue(proxyConfig, "type")
		if !ok {
			return true
		}
		for _, excludedType := range excludedTypes {
			if strings.EqualFold(proxyType, excludedType) {
				return true
			}
		}
	}
	for _, excludeRegexp := range excludeRegexps {
		matches, err := excludeRegexp.MatchString(proxyName)
		if err == nil && matches {
			return true
		}
	}
	return false
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
			compiled, err := regexp2.Compile(pattern, regexp2.None)
			if err != nil {
				return fmt.Errorf("invalid proxy-name override regex: %w", err)
			}
			name, err = compiled.Replace(name, target, 0, -1)
			if err != nil {
				return fmt.Errorf("apply proxy-name override: %w", err)
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
	ProxyName        string         `json:"proxy_name"`
	ProxyType        string         `json:"proxy_type"`
	ProxyConfig      map[string]any `json:"proxy_config"`
	Latency          time.Duration  `json:"latency"`
	Jitter           time.Duration  `json:"jitter"`
	PacketLoss       float64        `json:"packet_loss"`
	DownloadSize     float64        `json:"download_size"`
	DownloadTime     time.Duration  `json:"download_time"`
	DownloadSpeed    float64        `json:"download_speed"`
	DownloadError    string         `json:"download_error"`
	DownloadTested   bool           `json:"download_tested"`
	DownloadComplete bool           `json:"download_complete"`
	UploadSize       float64        `json:"upload_size"`
	UploadTime       time.Duration  `json:"upload_time"`
	UploadSpeed      float64        `json:"upload_speed"`
	UploadError      string         `json:"upload_error"`
	UploadTested     bool           `json:"upload_tested"`
	UploadComplete   bool           `json:"upload_complete"`
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

func (r *Result) FormatPacketLoss() string {
	return fmt.Sprintf("%.1f%%", r.PacketLoss)
}

func (r *Result) FormatUploadSpeed() string {
	if r.UploadSpeed <= 0 && r.UploadError != "" {
		return r.UploadError
	}
	return formatSpeed(r.UploadSpeed)
}

func (r *Result) FormatUploadSpeedValue() string {
	return formatSpeed(r.UploadSpeed)
}

func (r *Result) FormatDownloadError() string {
	if r.DownloadError == "" {
		return "N/A"
	}
	return r.DownloadError
}

func (r *Result) FormatUploadError() string {
	if r.UploadError == "" {
		return "N/A"
	}
	return r.UploadError
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
	result.PacketLoss = latencyResult.packetLoss
	return result
}

// ShouldTestTransfers determines whether a probed node should enter the
// bandwidth-intensive phase. GUI runs always have an output path and apply
// configured thresholds before consuming transfer traffic; CLI display-only
// runs retain the historical behavior of testing every reachable node.
func (st *SpeedTester) ShouldTestTransfers(result *Result) bool {
	if result == nil || st.mode.IsFast() || result.Latency <= 0 || result.PacketLoss >= 100 {
		return false
	}
	if st.config.OutputPath != "" && st.config.MaxPacketLoss < 100 && result.PacketLoss > st.config.MaxPacketLoss {
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
	var uploadSummary *transferSummary
	if st.mode.UploadEnabled() {
		uploadSummary = newTransferSummary()
	}

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
				downloadResults <- st.testDownload(proxy, size, st.config.Timeout)
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

		if !result.DownloadComplete || result.DownloadError != "" {
			return
		}
		if st.config.OutputPath != "" && st.config.MinDownloadSpeed > 0 && result.DownloadSpeed < st.config.MinDownloadSpeed {
			return
		}
	}

	if st.mode.UploadEnabled() {
		uploadChunks := splitTransferSizes(st.config.UploadSize, st.config.Concurrent)
		if len(uploadChunks) > 0 {
			result.UploadTested = true
			uploadResults := make(chan *downloadResult, len(uploadChunks))
			batchStarted := time.Now()
			var wg sync.WaitGroup
			for _, chunkSize := range uploadChunks {
				wg.Add(1)
				go func(size int) {
					defer wg.Done()
					uploadResults <- st.testUpload(proxy, size, st.config.Timeout)
				}(chunkSize)
			}
			wg.Wait()
			batchDuration := time.Since(batchStarted)
			for range uploadChunks {
				if ur := <-uploadResults; ur != nil {
					uploadSummary.add(ur)
				}
			}
			close(uploadResults)
			result.UploadSize, result.UploadTime, result.UploadSpeed, result.UploadError, result.UploadComplete =
				applyTransferSummary(uploadSummary, batchDuration, len(uploadChunks))
		}
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
	return st.config.Timeout
}

type latencyResult struct {
	latency    time.Duration
	jitter     time.Duration
	packetLoss float64
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
		if startErr != nil || endErr != nil || start != 0 || end < int64(size-1) {
			return 0, fmt.Errorf("Content-Range does not cover requested bytes")
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

func (st *SpeedTester) testUpload(proxy constant.Proxy, size int, timeout time.Duration) *downloadResult {
	client := st.createClient(proxy, timeout)
	defer client.CloseIdleConnections()

	reader := NewZeroReader(size)
	uploadURL := fmt.Sprintf("%s/__up", st.serverBaseURL)

	start := time.Now()
	req, err := http.NewRequest(http.MethodPost, uploadURL, reader)
	if err != nil {
		return &downloadResult{error: fmt.Sprintf("create upload request for %s failed: %v", uploadURL, err)}
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(size)
	resp, err := client.Do(req)
	if err != nil {
		return &downloadResult{
			bytes:    reader.WrittenBytes(),
			duration: time.Since(start),
			error:    fmt.Sprintf("upload request to %s failed: %v, spent %s", uploadURL, err, time.Since(start)),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &downloadResult{
			bytes:    reader.WrittenBytes(),
			duration: time.Since(start),
			error:    fmt.Sprintf("upload response from %s returned %s, spent %s", uploadURL, resp.Status, time.Since(start)),
		}
	}
	if reader.WrittenBytes() != int64(size) {
		return &downloadResult{
			bytes:    reader.WrittenBytes(),
			duration: time.Since(start),
			error: fmt.Sprintf("upload to %s was incomplete: wrote %d bytes, want %d, spent %s",
				uploadURL, reader.WrittenBytes(), size, time.Since(start)),
		}
	}

	return &downloadResult{
		bytes:    reader.WrittenBytes(),
		duration: time.Since(start),
	}
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
		packetLoss: 100,
	}
	if totalProbes > 0 {
		result.packetLoss = float64(failedPings) / float64(totalProbes) * 100
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

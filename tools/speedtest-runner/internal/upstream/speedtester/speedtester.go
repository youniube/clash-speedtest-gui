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

const maxHTTPConfigSize = 32 * 1024 * 1024

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
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
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
				log.Printf("failed to fetch config: %s", err)
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
			return nil, fmt.Errorf("unable to parse config at path %s: %w, body: %s", configPath, err, body)
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
			pd, err := provider.ParseProxyProvider(name, config, nil)
			if err != nil {
				return nil, fmt.Errorf("parse proxy provider %s error: %w", name, err)
			}
			if err := pd.Initial(); err != nil {
				log.Printf("initial proxy provider %s error: %s", pd.Name(), err)
				continue
			}

			providerURL, ok := stringMapValue(config, "url")
			if !ok || strings.TrimSpace(providerURL) == "" {
				log.Printf("skip proxy provider %s: missing url", name)
				continue
			}
			body, err = st.fetchHTTPConfig(providerURL)
			if err != nil {
				log.Printf("failed to fetch config: %s", err)
				continue
			}
			pdRawCfg := &RawConfig{
				Proxies: []map[string]any{},
			}
			if err := yaml.Unmarshal(body, pdRawCfg); err != nil {
				return nil, fmt.Errorf("unable to parse config: %w, body: %s", err, body)
			}
			pdProxies := make(map[string]map[string]any)
			for _, pdProxy := range pdRawCfg.Proxies {
				proxyName, ok := stringMapValue(pdProxy, "name")
				if !ok {
					continue
				}
				if _, ok := pdProxy["server"]; !ok {
					continue
				}
				pdProxies[proxyName] = pdProxy
			}
			for _, proxy := range pd.Proxies() {
				proxies[fmt.Sprintf("[%s] %s", name, proxy.Name())] = &CProxy{
					Proxy:  proxy,
					Config: pdProxies[proxy.Name()],
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
	for name, proxy := range proxies {
		tester(st.testProxy(name, proxy))
	}
}

type Result struct {
	ProxyName     string         `json:"proxy_name"`
	ProxyType     string         `json:"proxy_type"`
	ProxyConfig   map[string]any `json:"proxy_config"`
	Latency       time.Duration  `json:"latency"`
	Jitter        time.Duration  `json:"jitter"`
	PacketLoss    float64        `json:"packet_loss"`
	DownloadSize  float64        `json:"download_size"`
	DownloadTime  time.Duration  `json:"download_time"`
	DownloadSpeed float64        `json:"download_speed"`
	DownloadError string         `json:"download_error"`
	UploadSize    float64        `json:"upload_size"`
	UploadTime    time.Duration  `json:"upload_time"`
	UploadSpeed   float64        `json:"upload_speed"`
	UploadError   string         `json:"upload_error"`
}

func (r *Result) FormatDownloadSpeed() string {
	if r.DownloadError != "" {
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
	if r.UploadError != "" {
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
	result := &Result{
		ProxyName:   name,
		ProxyType:   proxy.Type().String(),
		ProxyConfig: proxy.Config,
	}

	// 1. 首先进行延迟测试
	latencyResult := st.testLatency(proxy, st.latencyRequestTimeout())
	result.Latency = latencyResult.avgLatency
	result.Jitter = latencyResult.jitter
	result.PacketLoss = latencyResult.packetLoss

	if st.mode.IsFast() || result.PacketLoss == 100 {
		return result
	}
	if st.config.OutputPath != "" && st.config.MaxPacketLoss < 100 && latencyResult.packetLoss > st.config.MaxPacketLoss {
		return result
	}
	if st.config.OutputPath != "" && st.config.MaxLatency > 0 && latencyResult.avgLatency > st.config.MaxLatency {
		return result
	}

	// 2. 并发进行下载测试，按需进行上传测试

	var wg sync.WaitGroup

	downloadSummary := newTransferSummary()
	var uploadSummary *transferSummary
	if st.mode.UploadEnabled() {
		uploadSummary = newTransferSummary()
	}

	downloadChunkSize := st.config.DownloadSize / st.config.Concurrent
	if downloadChunkSize > 0 {
		downloadResults := make(chan *downloadResult, st.config.Concurrent)
		batchStarted := time.Now()

		for i := 0; i < st.config.Concurrent; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				downloadResults <- st.testDownload(proxy, downloadChunkSize, st.config.Timeout)
			}()
		}
		wg.Wait()
		batchDuration := time.Since(batchStarted)

		for range st.config.Concurrent {
			if dr := <-downloadResults; dr != nil {
				downloadSummary.add(dr)
			}
		}
		close(downloadResults)

		result.DownloadSize, result.DownloadTime, result.DownloadSpeed, result.DownloadError = applyTransferSummary(downloadSummary, batchDuration)

		if st.config.OutputPath != "" && st.config.MinDownloadSpeed > 0 && result.DownloadSpeed < st.config.MinDownloadSpeed {
			return result
		}
	}

	if st.mode.UploadEnabled() {
		uploadChunkSize := st.config.UploadSize / st.config.Concurrent
		if uploadChunkSize > 0 {
			uploadResults := make(chan *downloadResult, st.config.Concurrent)
			batchStarted := time.Now()

			for i := 0; i < st.config.Concurrent; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					uploadResults <- st.testUpload(proxy, uploadChunkSize, st.config.Timeout)
				}()
			}
			wg.Wait()
			batchDuration := time.Since(batchStarted)

			for i := 0; i < st.config.Concurrent; i++ {
				if ur := <-uploadResults; ur != nil {
					uploadSummary.add(ur)
				}
			}
			close(uploadResults)

			result.UploadSize, result.UploadTime, result.UploadSpeed, result.UploadError = applyTransferSummary(uploadSummary, batchDuration)
		}
	}

	return result
}

func (st *SpeedTester) latencyRequestTimeout() time.Duration {
	return st.config.Timeout
}

type latencyResult struct {
	avgLatency time.Duration
	jitter     time.Duration
	packetLoss float64
}

func (st *SpeedTester) testLatency(proxy constant.Proxy, requestTimeout time.Duration) *latencyResult {
	client := st.createClient(proxy, requestTimeout)
	defer client.CloseIdleConnections()

	latencies := make([]time.Duration, 0, 6)
	failedPings := 0

	for range 6 {
		time.Sleep(100 * time.Millisecond)

		start := time.Now()
		req, err := http.NewRequest(http.MethodHead, st.downloadURL, nil)
		if err != nil {
			failedPings++
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			failedPings++
			continue
		}
		resp.Body.Close()
		latencies = append(latencies, time.Since(start))
	}

	return calculateLatencyStats(latencies, failedPings)
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

func applyTransferSummary(summary *transferSummary, batchDuration time.Duration) (float64, time.Duration, float64, string) {
	if summary == nil {
		return 0, 0, 0, ""
	}
	var size float64
	var duration time.Duration
	var speed float64
	var errorMessage string
	if summary.successCount > 0 {
		size = float64(summary.totalBytes)
		duration = batchDuration
		if duration > 0 {
			speed = float64(summary.totalBytes) / duration.Seconds()
		}
	}
	if len(summary.errors) > 0 {
		errorMessage = strings.Join(summary.errors, "; ")
		// If any transfer error is reported, treat the speed as zero.
		speed = 0
	}
	return size, duration, speed, errorMessage
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
	if result.error != "" {
		s.appendError(result.error)
		return
	}
	s.totalBytes += result.bytes
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
			error: fmt.Sprintf("download response from %s is invalid: %v, spent %s", downloadURL, readErr, time.Since(start)),
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
	resp, err := client.Post(
		uploadURL,
		"application/octet-stream",
		reader,
	)
	if err != nil {
		return &downloadResult{
			error: fmt.Sprintf("upload request to %s failed: %v, spent %s", uploadURL, err, time.Since(start)),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &downloadResult{
			error: fmt.Sprintf("upload response from %s returned %s, spent %s", uploadURL, resp.Status, time.Since(start)),
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

func calculateLatencyStats(latencies []time.Duration, failedPings int) *latencyResult {
	result := &latencyResult{
		packetLoss: float64(failedPings) / 6.0 * 100,
	}

	if len(latencies) == 0 {
		return result
	}

	// 计算平均延迟
	var total time.Duration
	for _, l := range latencies {
		total += l
	}
	result.avgLatency = total / time.Duration(len(latencies))

	// 计算抖动
	var variance float64
	for _, l := range latencies {
		diff := float64(l - result.avgLatency)
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

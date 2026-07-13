package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"clash-speedtest.local/speedtest-runner/internal/shareurl"
	"clash-speedtest.local/speedtest-runner/internal/upstream/output"
	"clash-speedtest.local/speedtest-runner/internal/upstream/speedtester"
	"gopkg.in/yaml.v2"
)

var (
	configPaths      = flag.String("c", "", "configuration file paths")
	filterRegex      = flag.String("f", ".+", "filter proxies by name")
	blockKeywords    = flag.String("b", "", "block proxies by keywords")
	serverURL        = flag.String("server-url", "https://dl.google.com/chrome/mac/universal/stable/GGRO/googlechrome.dmg", "speed test URL")
	speedMode        = flag.String("speed-mode", "download", "fast or download")
	downloadSize     = flag.Int("download-size", 50*1024*1024, "download size")
	probeTimeout     = flag.Duration("probe-timeout", 3*time.Second, "timeout for each HTTP probe request")
	timeout          = flag.Duration("timeout", 8*time.Second, "download transfer timeout")
	transferParallel = flag.Int("concurrent", 4, "download connections per node")
	nodeParallel     = flag.Int("node-concurrent", 4, "nodes probed concurrently")
	outputPath       = flag.String("output", "", "output Clash YAML path")
	maxLatency       = flag.Duration("max-latency", time.Second, "maximum latency")
	maxProbeFailure  = flag.Float64("max-probe-failure", 100, "maximum HTTP probe failure rate in percent")
	minDownloadSpeed = flag.Float64("min-download-speed", 5, "minimum download speed in MB/s")
	userAgent        = flag.String("ua", "", "User-Agent")
	listConfigPath   = flag.String("list-config", "", "list nodes from a local Clash YAML file as JSON")
	manageConfigPath = flag.String("manage-config", "", "apply local node management operations from JSON")
	regionQueryPath  = flag.String("region-query", "", "query exit regions for node IDs from JSON")
	versionFlag      = flag.Bool("v", false, "show version")
)

var legacyTSVSanitizer = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")

const (
	maxManagedFileSize        = 32 * 1024 * 1024
	speedEventProtocolVersion = 5
)

type proxyJob struct {
	name  string
	proxy *speedtester.CProxy
}

type nodeEvent struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	ShareURL   string         `json:"share_url,omitempty"`
	ShareError string         `json:"share_error,omitempty"`
	Config     map[string]any `json:"config"`
}

type resultEvent struct {
	ID      string        `json:"id"`
	Cells   []string      `json:"cells"`
	Usable  bool          `json:"usable"`
	Metrics resultMetrics `json:"metrics"`
}

type resultMetrics struct {
	LatencyNanoseconds      int64   `json:"latency_nanoseconds"`
	JitterNanoseconds       int64   `json:"jitter_nanoseconds"`
	HTTPProbeFailurePercent float64 `json:"http_probe_failure_percent"`
	DownloadBytesPerSecond  float64 `json:"download_bytes_per_second"`
	DownloadTested          bool    `json:"download_tested"`
	DownloadComplete        bool    `json:"download_complete"`
}

type progressEvent struct {
	ID    string `json:"id"`
	Stage string `json:"stage"`
}

type managedConfig struct {
	Proxies []map[string]any `yaml:"proxies"`
}

type nodeManagementRequest struct {
	Renames map[string]string `json:"renames"`
	Deletes []string          `json:"deletes"`
}

type nodeManagementResult struct {
	Renamed int         `json:"renamed"`
	Deleted int         `json:"deleted"`
	Nodes   []nodeEvent `json:"nodes"`
}

func main() {
	flag.Parse()
	if *versionFlag {
		fmt.Println("speedtest-runner version 2.0.0 (clash-speedtest v1.8.8 adapted, mihomo v1.19.27)")
		return
	}
	if strings.TrimSpace(*listConfigPath) != "" {
		result, err := listManagedConfig(strings.TrimSpace(*listConfigPath))
		if err != nil {
			fail("list config failed: " + err.Error())
		}
		writeJSONResult(result)
		return
	}
	if strings.TrimSpace(*manageConfigPath) != "" {
		if strings.TrimSpace(*configPaths) == "" || strings.TrimSpace(*outputPath) == "" {
			fail("manage config requires -c and -output")
		}
		result, err := manageLocalConfig(
			strings.TrimSpace(*configPaths),
			strings.TrimSpace(*outputPath),
			strings.TrimSpace(*manageConfigPath),
		)
		if err != nil {
			fail("manage config failed: " + err.Error())
		}
		writeJSONResult(result)
		return
	}
	if strings.TrimSpace(*regionQueryPath) != "" {
		if strings.TrimSpace(*configPaths) == "" {
			fail("region query requires -c")
		}
		if err := runRegionQuery(
			strings.TrimSpace(*configPaths),
			strings.TrimSpace(*regionQueryPath),
			os.Stdout,
		); err != nil {
			fail("region query failed: " + err.Error())
		}
		return
	}
	if strings.TrimSpace(*configPaths) == "" {
		fail("configuration file is required")
	}
	if err := validateFilterRegex(*filterRegex); err != nil {
		fail(err.Error())
	}

	mode, err := speedtester.ParseSpeedMode(*speedMode)
	if err != nil {
		fail("invalid speed mode")
	}
	if err := validateSpeedOptions(mode); err != nil {
		fail("invalid speed test options: " + err.Error())
	}
	tester, err := speedtester.New(&speedtester.Config{
		ConfigPaths:         *configPaths,
		FilterRegex:         *filterRegex,
		BlockRegex:          *blockKeywords,
		ServerURL:           *serverURL,
		DownloadSize:        *downloadSize,
		ProbeTimeout:        *probeTimeout,
		DownloadTimeout:     *timeout,
		Concurrent:          *transferParallel,
		MaxLatency:          *maxLatency,
		MaxHTTPProbeFailure: *maxProbeFailure,
		MinDownloadSpeed:    *minDownloadSpeed * 1024 * 1024,
		Mode:                mode,
		OutputPath:          *outputPath,
		UserAgent:           *userAgent,
	})
	if err != nil {
		fail("create speed tester failed: " + err.Error())
	}
	mode = tester.Mode()

	proxies, err := tester.LoadProxies()
	if err != nil {
		fail("load proxies failed: " + err.Error())
	}
	proxies = deduplicateProxiesByConfig(proxies)
	if len(proxies) == 0 {
		fail("no proxies matched the current configuration and filters")
	}

	writer := bufio.NewWriter(os.Stdout)
	if _, err := fmt.Fprintf(writer, "@protocol\t%d\n", speedEventProtocolVersion); err != nil {
		fail("write output protocol failed")
	}
	if _, err := writer.WriteString(strings.Join(output.GetHeaders(mode), "\t") + "\n"); err != nil {
		fail("write output header failed")
	}
	nodeIDs, err := writeManifest(writer, proxies)
	if err != nil {
		fail("write node manifest failed: " + err.Error())
	}
	if err := writer.Flush(); err != nil {
		fail("flush node manifest failed: " + err.Error())
	}

	results, err := testInStages(tester, proxies, nodeIDs, mode, writer, *nodeParallel)
	if err != nil {
		fail("write speed test results failed: " + err.Error())
	}
	results = output.SortResults(results, mode)

	if *outputPath != "" {
		if err := saveConfig(results, mode); err != nil {
			fail("save config failed: " + err.Error())
		}
		fmt.Printf("save config file to: %s\n", *outputPath)
	}
}

func validateFilterRegex(expression string) error {
	if _, err := regexp.Compile(expression); err != nil {
		return fmt.Errorf("invalid node filter regex: %w", err)
	}
	return nil
}

func validateSpeedOptions(mode speedtester.SpeedMode) error {
	if *probeTimeout <= 0 {
		return fmt.Errorf("probe timeout must be positive")
	}
	if *timeout <= 0 {
		return fmt.Errorf("download timeout must be positive")
	}
	if *nodeParallel < 1 || *nodeParallel > 128 {
		return fmt.Errorf("node concurrency must be between 1 and 128")
	}
	if *transferParallel < 1 || *transferParallel > speedtester.MaxTransferConcurrency {
		return fmt.Errorf("transfer concurrency must be between 1 and %d", speedtester.MaxTransferConcurrency)
	}
	if *maxLatency < 0 {
		return fmt.Errorf("maximum latency cannot be negative")
	}
	if math.IsNaN(*maxProbeFailure) || math.IsInf(*maxProbeFailure, 0) || *maxProbeFailure < 0 || *maxProbeFailure > 100 {
		return fmt.Errorf("maximum HTTP probe failure rate must be between 0 and 100")
	}
	if math.IsNaN(*minDownloadSpeed) || math.IsInf(*minDownloadSpeed, 0) || *minDownloadSpeed < 0 {
		return fmt.Errorf("minimum download speed must be a finite non-negative number")
	}
	if *downloadSize < 0 || (!mode.IsFast() && *downloadSize <= 0) {
		return fmt.Errorf("download size must be positive in %s mode", mode)
	}
	return nil
}

func deduplicateProxiesByConfig(proxies map[string]*speedtester.CProxy) map[string]*speedtester.CProxy {
	names := make([]string, 0, len(proxies))
	for name := range proxies {
		names = append(names, name)
	}
	sort.Strings(names)

	deduplicated := make(map[string]*speedtester.CProxy, len(proxies))
	seen := make(map[[sha256.Size]byte]struct{}, len(proxies))
	for _, name := range names {
		proxy := proxies[name]
		fingerprint, err := proxyConfigFingerprint(proxy)
		if err != nil {
			deduplicated[name] = proxy
			continue
		}
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		seen[fingerprint] = struct{}{}
		deduplicated[name] = proxy
	}
	return deduplicated
}

func proxyConfigFingerprint(proxy *speedtester.CProxy) ([sha256.Size]byte, error) {
	if proxy == nil || proxy.Config == nil {
		return [sha256.Size]byte{}, fmt.Errorf("proxy configuration is empty")
	}
	return configFingerprint(proxy.Config)
}

func configFingerprint(values map[string]any) ([sha256.Size]byte, error) {
	if values == nil {
		return [sha256.Size]byte{}, fmt.Errorf("proxy configuration is empty")
	}

	config := make(map[string]any, len(values))
	for key, value := range values {
		if key != "name" {
			config[key] = value
		}
	}
	canonical, err := yaml.Marshal(config)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}

func listManagedConfig(path string) (*nodeManagementResult, error) {
	config, err := loadManagedConfig(path)
	if err != nil {
		return nil, err
	}
	return describeManagedConfig(config, 0, 0)
}

func manageLocalConfig(inputPath, outputPath, requestPath string) (*nodeManagementResult, error) {
	config, err := loadManagedConfig(inputPath)
	if err != nil {
		return nil, err
	}
	requestData, err := readFileLimited(requestPath, maxManagedFileSize)
	if err != nil {
		return nil, fmt.Errorf("read management request: %w", err)
	}
	var request nodeManagementRequest
	if err := json.Unmarshal(requestData, &request); err != nil {
		return nil, fmt.Errorf("decode management request: %w", err)
	}

	updated, renamed, deleted, err := applyNodeManagement(config.Proxies, &request)
	if err != nil {
		return nil, err
	}
	config.Proxies = updated
	yamlData, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode managed config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(outputPath, yamlData, 0o600); err != nil {
		return nil, fmt.Errorf("write managed config: %w", err)
	}
	return describeManagedConfig(config, renamed, deleted)
}

func loadManagedConfig(path string) (*managedConfig, error) {
	body, err := readFileLimited(path, maxManagedFileSize)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var config managedConfig
	if err := yaml.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if config.Proxies == nil {
		config.Proxies = []map[string]any{}
	}
	return &config, nil
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return body, nil
}

func applyNodeManagement(
	proxies []map[string]any,
	request *nodeManagementRequest,
) ([]map[string]any, int, int, error) {
	if request == nil {
		request = &nodeManagementRequest{}
	}
	deleteIDs := make(map[string]struct{}, len(request.Deletes))
	requested := make(map[string]struct{}, len(request.Deletes)+len(request.Renames))
	for _, id := range request.Deletes {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, 0, 0, fmt.Errorf("delete id is empty")
		}
		if _, editing := request.Renames[id]; editing {
			return nil, 0, 0, fmt.Errorf("node %s cannot be renamed and deleted together", id)
		}
		deleteIDs[id] = struct{}{}
		requested[id] = struct{}{}
	}
	for id, name := range request.Renames {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, 0, 0, fmt.Errorf("rename id is empty")
		}
		if strings.TrimSpace(name) == "" {
			return nil, 0, 0, fmt.Errorf("new node name is empty")
		}
		requested[id] = struct{}{}
	}

	matched := make(map[string]struct{}, len(requested))
	updated := make([]map[string]any, 0, len(proxies))
	renamed := 0
	deleted := 0
	for _, proxy := range proxies {
		fingerprint, err := configFingerprint(proxy)
		if err != nil {
			return nil, 0, 0, err
		}
		id := fmt.Sprintf("%x", fingerprint)
		if _, remove := deleteIDs[id]; remove {
			matched[id] = struct{}{}
			deleted++
			continue
		}
		if name, change := request.Renames[id]; change {
			proxy["name"] = strings.TrimSpace(name)
			matched[id] = struct{}{}
			renamed++
		}
		updated = append(updated, proxy)
	}
	for id := range requested {
		if _, ok := matched[id]; !ok {
			return nil, 0, 0, fmt.Errorf("node %s was not found in the current output", id)
		}
	}

	names := make(map[string]struct{}, len(updated))
	for _, proxy := range updated {
		name, ok := proxy["name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, 0, 0, fmt.Errorf("managed node has an invalid name")
		}
		if _, duplicate := names[name]; duplicate {
			return nil, 0, 0, fmt.Errorf("duplicate node name: %s", name)
		}
		names[name] = struct{}{}
	}
	return updated, renamed, deleted, nil
}

func describeManagedConfig(
	config *managedConfig,
	renamed int,
	deleted int,
) (*nodeManagementResult, error) {
	result := &nodeManagementResult{
		Renamed: renamed,
		Deleted: deleted,
		Nodes:   make([]nodeEvent, 0, len(config.Proxies)),
	}
	for _, proxy := range config.Proxies {
		compatible := jsonCompatibleMap(proxy)
		fingerprint, err := configFingerprint(proxy)
		if err != nil {
			return nil, err
		}
		name, nameOK := compatible["name"].(string)
		proxyType, typeOK := compatible["type"].(string)
		if !nameOK || strings.TrimSpace(name) == "" || !typeOK || strings.TrimSpace(proxyType) == "" {
			return nil, fmt.Errorf("managed node name or type is invalid")
		}
		shareURL, shareErr := shareurl.Generate(compatible)
		event := nodeEvent{
			ID:       fmt.Sprintf("%x", fingerprint),
			Name:     name,
			Type:     proxyType,
			ShareURL: shareURL,
			Config:   compatible,
		}
		if shareErr != nil {
			event.ShareError = shareErr.Error()
		}
		result.Nodes = append(result.Nodes, event)
	}
	return result, nil
}

func writeJSONResult(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fail("write JSON result failed: " + err.Error())
	}
}

func writeManifest(writer *bufio.Writer, proxies map[string]*speedtester.CProxy) (map[string]string, error) {
	names := make([]string, 0, len(proxies))
	for name := range proxies {
		names = append(names, name)
	}
	sort.Strings(names)
	nodeIDs := make(map[string]string, len(names))
	seenIDs := make(map[string]string, len(names))
	if _, err := fmt.Fprintf(writer, "@nodes\t%d\n", len(names)); err != nil {
		return nil, err
	}
	for _, name := range names {
		proxy := proxies[name]
		if proxy == nil {
			return nil, fmt.Errorf("proxy %q is nil", name)
		}
		proxyType := proxy.Type().String()
		id := nodeID(name, proxy)
		if previous, exists := seenIDs[id]; exists {
			return nil, fmt.Errorf("node ID collision between %q and %q", previous, name)
		}
		seenIDs[id] = name
		nodeIDs[name] = id
		eventConfig := jsonCompatibleMap(proxy.Config)
		eventConfig["name"] = name
		shareURL, shareErr := shareurl.Generate(eventConfig)
		event := nodeEvent{
			ID:       id,
			Name:     name,
			Type:     proxyType,
			ShareURL: shareURL,
			Config:   eventConfig,
		}
		if shareErr != nil {
			event.ShareError = shareErr.Error()
		}
		if err := writeEvent(writer, "@nodejson", event); err != nil {
			return nil, fmt.Errorf("write node %q event: %w", name, err)
		}
		encodedName := base64.RawStdEncoding.EncodeToString([]byte(name))
		if _, err := fmt.Fprintf(writer, "@node\t%s\t%s\n", encodedName, proxyType); err != nil {
			return nil, fmt.Errorf("write node %q legacy row: %w", name, err)
		}
	}
	return nodeIDs, nil
}

func testInStages(
	tester *speedtester.SpeedTester,
	proxies map[string]*speedtester.CProxy,
	nodeIDs map[string]string,
	mode speedtester.SpeedMode,
	writer *bufio.Writer,
	parallel int,
) ([]*speedtester.Result, error) {
	if parallel > len(proxies) {
		parallel = len(proxies)
	}
	if parallel < 1 {
		return nil, fmt.Errorf("node concurrency must be positive")
	}
	names := make([]string, 0, len(proxies))
	for name := range proxies {
		names = append(names, name)
	}
	sort.Strings(names)

	jobs := make(chan proxyJob)
	type probeOutput struct {
		name   string
		result *speedtester.Result
	}
	resultChannel := make(chan probeOutput, len(proxies))
	var workers sync.WaitGroup

	for range parallel {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				resultChannel <- probeOutput{
					name:   job.name,
					result: tester.ProbeProxy(job.name, job.proxy),
				}
			}
		}()
	}

	go func() {
		for _, name := range names {
			proxy := proxies[name]
			jobs <- proxyJob{name: name, proxy: proxy}
		}
		close(jobs)
		workers.Wait()
		close(resultChannel)
	}()

	results := make([]*speedtester.Result, 0, len(proxies))
	seenProbeIDs := make(map[string]struct{}, len(proxies))
	seenResultIDs := make(map[string]struct{}, len(proxies))

	type downloadJob struct {
		name   string
		result *speedtester.Result
	}
	downloadDone := make(chan downloadJob, 1)
	pendingDownloads := make([]downloadJob, 0, len(proxies))
	downloadRunning := false
	probeChannel := resultChannel

	writeProgress := func(name, stage string) error {
		id, known := nodeIDs[name]
		if !known || id == "" {
			return fmt.Errorf("progress references unknown proxy %q", name)
		}
		if err := writeEvent(writer, "@progressjson", progressEvent{ID: id, Stage: stage}); err != nil {
			return fmt.Errorf("write %s progress for proxy %q: %w", stage, name, err)
		}
		if err := writer.Flush(); err != nil {
			return fmt.Errorf("flush %s progress for proxy %q: %w", stage, name, err)
		}
		return nil
	}
	writeResult := func(result *speedtester.Result) error {
		if result == nil {
			return fmt.Errorf("speed tester returned a nil result")
		}
		id, known := nodeIDs[result.ProxyName]
		if !known || id == "" {
			return fmt.Errorf("result references unknown proxy %q", result.ProxyName)
		}
		if _, duplicate := seenResultIDs[id]; duplicate {
			return fmt.Errorf("duplicate result for proxy %q", result.ProxyName)
		}
		seenResultIDs[id] = struct{}{}
		results = append(results, result)
		row := output.FormatRow(result, mode, len(results)-1)
		event := buildResultEvent(id, row, result, mode, isUsable(result, mode))
		if err := writeEvent(writer, "@resultjson", event); err != nil {
			return fmt.Errorf("write result event for proxy %q: %w", result.ProxyName, err)
		}
		if _, err := writer.WriteString(strings.Join(sanitizeLegacyTSVCells(row), "\t") + "\n"); err != nil {
			return fmt.Errorf("write result row for proxy %q: %w", result.ProxyName, err)
		}
		if err := writer.Flush(); err != nil {
			return fmt.Errorf("flush result row for proxy %q: %w", result.ProxyName, err)
		}
		return nil
	}

	for len(seenResultIDs) < len(proxies) {
		if !downloadRunning && len(pendingDownloads) > 0 {
			job := pendingDownloads[0]
			pendingDownloads = pendingDownloads[1:]
			if err := writeProgress(job.name, "download_started"); err != nil {
				return results, err
			}
			downloadRunning = true
			go func(job downloadJob) {
				tester.TestTransfers(job.result, proxies[job.name])
				downloadDone <- job
			}(job)
		}
		if probeChannel == nil && !downloadRunning && len(pendingDownloads) == 0 {
			return results, fmt.Errorf("speed result count mismatch: got %d, want %d", len(seenResultIDs), len(proxies))
		}

		select {
		case probed, ok := <-probeChannel:
			if !ok {
				probeChannel = nil
				continue
			}
			if probed.result == nil {
				return results, fmt.Errorf("speed tester returned a nil probe result for proxy %q", probed.name)
			}
			id, known := nodeIDs[probed.name]
			if !known || id == "" {
				return results, fmt.Errorf("probe references unknown proxy %q", probed.name)
			}
			if _, duplicate := seenProbeIDs[id]; duplicate {
				return results, fmt.Errorf("duplicate probe result for proxy %q", probed.name)
			}
			seenProbeIDs[id] = struct{}{}
			if err := writeProgress(probed.name, "probe_completed"); err != nil {
				return results, err
			}
			if tester.ShouldTestTransfers(probed.result) {
				pendingDownloads = append(pendingDownloads, downloadJob{
					name: probed.name, result: probed.result,
				})
			} else if err := writeResult(probed.result); err != nil {
				return results, err
			}
		case completed := <-downloadDone:
			downloadRunning = false
			if err := writeResult(completed.result); err != nil {
				return results, err
			}
		}
	}

	if len(seenProbeIDs) != len(proxies) {
		return results, fmt.Errorf("probe result count mismatch: got %d, want %d", len(seenProbeIDs), len(proxies))
	}
	return results, nil
}

func buildResultEvent(
	id string,
	row []string,
	result *speedtester.Result,
	mode speedtester.SpeedMode,
	usable bool,
) resultEvent {
	downloadTested := !mode.IsFast() && result.DownloadTested
	downloadSpeed := result.DownloadSpeed
	if !downloadTested {
		downloadSpeed = 0
	}
	return resultEvent{
		ID:     id,
		Cells:  row,
		Usable: usable,
		Metrics: resultMetrics{
			LatencyNanoseconds:      int64(result.Latency),
			JitterNanoseconds:       int64(result.Jitter),
			HTTPProbeFailurePercent: result.HTTPProbeFailurePercent,
			DownloadBytesPerSecond:  downloadSpeed,
			DownloadTested:          downloadTested,
			DownloadComplete:        downloadTested && result.DownloadComplete,
		},
	}
}

func sanitizeLegacyTSVCells(row []string) []string {
	sanitized := make([]string, len(row))
	for index, cell := range row {
		sanitized[index] = legacyTSVSanitizer.Replace(cell)
	}
	return sanitized
}

func nodeID(name string, proxy *speedtester.CProxy) string {
	fingerprint, err := proxyConfigFingerprint(proxy)
	if err == nil {
		return fmt.Sprintf("%x", fingerprint)
	}
	fallback := sha256.Sum256([]byte(name + "\x00" + proxy.Type().String()))
	return fmt.Sprintf("%x", fallback)
}

func writeEvent(writer *bufio.Writer, prefix string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded := base64.RawStdEncoding.EncodeToString(body)
	if _, err := fmt.Fprintf(writer, "%s\t%s\n", prefix, encoded); err != nil {
		return err
	}
	return nil
}

func jsonCompatibleMap(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = jsonCompatibleValue(value)
	}
	return result
}

func jsonCompatibleValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return jsonCompatibleMap(typed)
	case map[any]any:
		result := make(map[string]any, len(typed))
		for rawKey, item := range typed {
			key, ok := rawKey.(string)
			if ok {
				result[key] = jsonCompatibleValue(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = jsonCompatibleValue(item)
		}
		return result
	default:
		return value
	}
}

func saveConfig(results []*speedtester.Result, mode speedtester.SpeedMode) error {
	proxies := make([]map[string]any, 0, len(results))

	for _, result := range results {
		if !isUsable(result, mode) {
			continue
		}
		proxyConfig := result.ProxyConfig
		if proxyConfig == nil || proxyConfig["name"] == nil || proxyConfig["server"] == nil {
			continue
		}
		proxies = append(proxies, proxyConfig)
	}

	config := &speedtester.RawConfig{Proxies: proxies}
	yamlData, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*outputPath, yamlData, 0o600)
}

func isUsable(result *speedtester.Result, mode speedtester.SpeedMode) bool {
	if result == nil || result.Latency <= 0 || result.HTTPProbeFailurePercent >= 100 {
		return false
	}
	if *maxLatency > 0 && result.Latency > *maxLatency {
		return false
	}
	if *maxProbeFailure >= 0 && result.HTTPProbeFailurePercent > *maxProbeFailure {
		return false
	}
	if !mode.IsFast() && *downloadSize > 0 {
		if !result.DownloadTested || !result.DownloadComplete || result.DownloadError != "" || result.DownloadSpeed <= 0 {
			return false
		}
		if *minDownloadSpeed > 0 && result.DownloadSpeed < *minDownloadSpeed*1024*1024 {
			return false
		}
	}
	return true
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

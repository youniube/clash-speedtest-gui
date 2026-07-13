package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"clash-speedtest.local/speedtest-runner/internal/upstream/speedtester"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

const (
	regionProviderTimeout      = 4 * time.Second
	regionNodeBudget           = 12 * time.Second
	regionNodeConcurrent       = 8
	maxRegionResponseSize      = 1024 * 1024
	regionEventProtocolVersion = 2
	maxRegionRequestIDs        = 100000
)

type RegionResult struct {
	CountryCode string `json:"country_code"`
	Country     string `json:"country"`
	City        string `json:"city"`
	Emoji       string `json:"emoji"`
	Provider    string `json:"provider"`
}

type RegionProvider interface {
	Name() string
	Lookup(context.Context, *http.Client) (RegionResult, error)
}

type regionProviderState struct {
	Disabled bool
	Streak   int
}

type FallbackRegionProvider struct {
	Providers []RegionProvider
	mu        sync.Mutex
	states    map[string]regionProviderState
}

type regionQueryRequest struct {
	IDs []string `json:"ids"`
}

type regionEvent struct {
	NodeID      string `json:"node_id"`
	Success     bool   `json:"success"`
	CountryCode string `json:"country_code,omitempty"`
	Country     string `json:"country,omitempty"`
	City        string `json:"city,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Error       string `json:"error,omitempty"`
}

type regionHTTPClientFactory func(*speedtester.CProxy) *http.Client

type regionEventTracker struct {
	expected map[string]struct{}
	seen     map[string]struct{}
}

func normalizeRegionRequestIDs(ids []string) ([]string, error) {
	if len(ids) > maxRegionRequestIDs {
		return nil, fmt.Errorf("too many region request IDs: %d", len(ids))
	}
	normalized := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for index, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, fmt.Errorf("region request ID %d is empty", index+1)
		}
		if id != rawID {
			return nil, fmt.Errorf("region request ID %d is not normalized", index+1)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("duplicate region request ID %q", id)
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("ids is empty")
	}
	return normalized, nil
}

func decodeRegionQueryRequest(body []byte) (regionQueryRequest, error) {
	var request regionQueryRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("decode request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return request, fmt.Errorf("decode request: trailing JSON value")
	}
	ids, err := normalizeRegionRequestIDs(request.IDs)
	if err != nil {
		return request, err
	}
	request.IDs = ids
	return request, nil
}

func newRegionEventTracker(ids []string) (*regionEventTracker, error) {
	normalized, err := normalizeRegionRequestIDs(ids)
	if err != nil {
		return nil, err
	}
	expected := make(map[string]struct{}, len(normalized))
	for _, id := range normalized {
		expected[id] = struct{}{}
	}
	return &regionEventTracker{expected: expected, seen: make(map[string]struct{}, len(expected))}, nil
}

func (t *regionEventTracker) Accept(event regionEvent) error {
	if t == nil {
		return fmt.Errorf("region event tracker is nil")
	}
	if event.NodeID == "" || event.NodeID != strings.TrimSpace(event.NodeID) {
		return fmt.Errorf("region event has an invalid node ID")
	}
	if _, known := t.expected[event.NodeID]; !known {
		return fmt.Errorf("region event references unknown node ID %q", event.NodeID)
	}
	if _, duplicate := t.seen[event.NodeID]; duplicate {
		return fmt.Errorf("duplicate region event for node ID %q", event.NodeID)
	}
	if event.Success {
		if !isRegionCountryCode(event.CountryCode) || !isNormalizedRegionNonempty(event.Country) ||
			!isNormalizedRegionOptional(event.City) || !isNormalizedRegionOptional(event.Emoji) ||
			!isNormalizedRegionNonempty(event.Provider) || event.Error != "" {
			return fmt.Errorf("successful region event for %q is incomplete", event.NodeID)
		}
	} else {
		if !isNormalizedRegionNonempty(event.Error) {
			return fmt.Errorf("failed region event for %q has no error", event.NodeID)
		}
		if event.CountryCode != "" || event.Country != "" || event.City != "" ||
			event.Emoji != "" || event.Provider != "" {
			return fmt.Errorf("failed region event for %q contains success fields", event.NodeID)
		}
	}
	t.seen[event.NodeID] = struct{}{}
	return nil
}

func (t *regionEventTracker) ValidateCompletion() error {
	if t == nil {
		return fmt.Errorf("region event tracker is nil")
	}
	if len(t.seen) != len(t.expected) {
		return fmt.Errorf("region event count mismatch: got %d, want %d", len(t.seen), len(t.expected))
	}
	return nil
}

type providerError struct {
	StatusCode int
	Network    bool
	Message    string
}

func (e *providerError) Error() string { return e.Message }

func NewFallbackRegionProvider(providers ...RegionProvider) *FallbackRegionProvider {
	return &FallbackRegionProvider{Providers: providers, states: make(map[string]regionProviderState)}
}

func (f *FallbackRegionProvider) Lookup(ctx context.Context, client *http.Client) (RegionResult, error) {
	var failures []string
	for _, provider := range f.Providers {
		if err := ctx.Err(); err != nil {
			return RegionResult{}, err
		}
		if f.isDisabled(provider.Name()) {
			continue
		}
		providerCtx, cancel := context.WithTimeout(ctx, regionProviderTimeout)
		result, err := provider.Lookup(providerCtx, client)
		cancel()
		if err == nil {
			result = normalizeRegionResult(result, provider.Name())
			if result.CountryCode != "" && result.Country != "" {
				f.recordSuccess(provider.Name())
				return result, nil
			}
			err = &providerError{Message: "地区字段缺失"}
		}
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return RegionResult{}, ctx.Err()
		}
		f.recordFailure(provider.Name(), err)
		failures = append(failures, provider.Name()+": "+compactRegionError(err))
	}
	if err := ctx.Err(); err != nil {
		return RegionResult{}, err
	}
	if len(failures) == 0 {
		return RegionResult{}, fmt.Errorf("没有可用的地区查询服务")
	}
	return RegionResult{}, fmt.Errorf("%s", strings.Join(failures, "; "))
}

func (f *FallbackRegionProvider) isDisabled(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[name].Disabled
}

func (f *FallbackRegionProvider) recordSuccess(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.states[name]
	state.Streak = 0
	f.states[name] = state
}

func (f *FallbackRegionProvider) recordFailure(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.states[name]
	var typed *providerError
	if errors.As(err, &typed) && (typed.StatusCode == http.StatusForbidden || typed.StatusCode == http.StatusTooManyRequests) {
		state.Disabled = true
		state.Streak = 0
	} else if errors.As(err, &typed) && (typed.Network || typed.StatusCode >= 500) {
		state.Streak++
		if state.Streak >= 3 {
			state.Disabled = true
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		state.Streak++
		if state.Streak >= 3 {
			state.Disabled = true
		}
	} else {
		state.Streak = 0
	}
	f.states[name] = state
}

type ipWhoisRegionProvider struct{ endpoint string }

func (p ipWhoisRegionProvider) Name() string { return "IPWHOIS.io" }
func (p ipWhoisRegionProvider) Lookup(ctx context.Context, client *http.Client) (RegionResult, error) {
	var body struct {
		Success     bool   `json:"success"`
		Message     string `json:"message"`
		CountryCode string `json:"country_code"`
		Country     string `json:"country"`
		City        string `json:"city"`
		Flag        struct {
			Emoji string `json:"emoji"`
		} `json:"flag"`
	}
	if err := getRegionJSON(ctx, client, p.endpoint, &body); err != nil {
		return RegionResult{}, err
	}
	if !body.Success {
		return RegionResult{}, &providerError{Message: "业务失败: " + emptyRegionValue(body.Message)}
	}
	return RegionResult{CountryCode: body.CountryCode, Country: body.Country, City: body.City, Emoji: body.Flag.Emoji}, nil
}

type freeIPAPIRegionProvider struct{ endpoint string }

func (p freeIPAPIRegionProvider) Name() string { return "FreeIPAPI" }
func (p freeIPAPIRegionProvider) Lookup(ctx context.Context, client *http.Client) (RegionResult, error) {
	var body struct {
		CountryCode string `json:"countryCode"`
		Country     string `json:"countryName"`
		City        string `json:"cityName"`
	}
	if err := getRegionJSON(ctx, client, p.endpoint, &body); err != nil {
		return RegionResult{}, err
	}
	return RegionResult{CountryCode: body.CountryCode, Country: body.Country, City: body.City}, nil
}

type ipSBRegionProvider struct{ endpoint string }

func (p ipSBRegionProvider) Name() string { return "IP.SB" }
func (p ipSBRegionProvider) Lookup(ctx context.Context, client *http.Client) (RegionResult, error) {
	var body struct {
		CountryCode string `json:"country_code"`
		Country     string `json:"country"`
		City        string `json:"city"`
	}
	if err := getRegionJSON(ctx, client, p.endpoint, &body); err != nil {
		return RegionResult{}, err
	}
	return RegionResult{CountryCode: body.CountryCode, Country: body.Country, City: body.City}, nil
}

func defaultRegionProvider() *FallbackRegionProvider {
	return NewFallbackRegionProvider(
		ipWhoisRegionProvider{endpoint: "https://ipwho.is/?fields=success,message,country_code,country,city,flag.emoji&lang=zh-CN"},
		freeIPAPIRegionProvider{endpoint: "https://freeipapi.com/api/json"},
		ipSBRegionProvider{endpoint: "https://api.ip.sb/geoip"},
	)
}

func getRegionJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &providerError{Message: "创建请求失败: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Clash-SpeedTest-GUI/1.3")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return &providerError{Network: true, Message: "网络错误: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &providerError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxRegionResponseSize+1))
	if err := decoder.Decode(target); err != nil {
		return &providerError{Message: "JSON 错误: " + err.Error()}
	}
	return nil
}

func normalizeRegionResult(result RegionResult, provider string) RegionResult {
	result.CountryCode = strings.ToUpper(strings.TrimSpace(result.CountryCode))
	result.Country = strings.TrimSpace(result.Country)
	result.City = strings.TrimSpace(result.City)
	result.Emoji = strings.TrimSpace(result.Emoji)
	result.Provider = provider
	if !isRegionCountryCode(result.CountryCode) {
		result.CountryCode = ""
		result.Emoji = ""
		return result
	}
	if provider != "IPWHOIS.io" || result.Country == "" {
		if region, err := language.ParseRegion(result.CountryCode); err == nil {
			result.Country = display.SimplifiedChinese.Regions().Name(region)
		}
	}
	if result.Emoji == "" {
		result.Emoji = countryFlag(result.CountryCode)
	}
	return result
}

func isRegionCountryCode(code string) bool {
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' &&
		code[1] >= 'A' && code[1] <= 'Z'
}

func isNormalizedRegionNonempty(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func isNormalizedRegionOptional(value string) bool {
	return value == "" || value == strings.TrimSpace(value)
}

func countryFlag(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !isRegionCountryCode(code) {
		return ""
	}
	return string([]rune{rune(0x1F1E6) + rune(code[0]-'A'), rune(0x1F1E6) + rune(code[1]-'A')})
}

func emptyRegionValue(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "未提供原因"
}

func compactRegionError(err error) string {
	if err == nil {
		return "未知错误"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	if errors.Is(err, context.Canceled) {
		return "已取消"
	}
	return err.Error()
}

func runRegionQuery(configPath, requestPath string, output io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runRegionQueryContext(ctx, configPath, requestPath, output)
}

func runRegionQueryContext(ctx context.Context, configPath, requestPath string, output io.Writer) error {
	if ctx == nil {
		return fmt.Errorf("region query context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, err := readFileLimited(requestPath, maxManagedFileSize)
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	request, err := decodeRegionQueryRequest(body)
	if err != nil {
		return err
	}

	tester, err := speedtester.New(&speedtester.Config{
		ConfigPaths: configPath, FilterRegex: ".+", ServerURL: "https://example.invalid/",
		Timeout: regionProviderTimeout, Concurrent: 1, Mode: speedtester.SpeedModeFast,
	})
	if err != nil {
		return err
	}
	proxies, err := tester.LoadProxies()
	if err != nil {
		return err
	}
	proxies = deduplicateProxiesByConfig(proxies)
	byID := make(map[string]*speedtester.CProxy, len(proxies))
	for name, proxy := range proxies {
		if proxy == nil {
			return fmt.Errorf("proxy %q is nil", name)
		}
		id := nodeID(name, proxy)
		if _, exists := byID[id]; exists {
			return fmt.Errorf("duplicate stable node ID %q", id)
		}
		byID[id] = proxy
	}

	provider := defaultRegionProvider()
	results := queryRegionEvents(ctx, request.IDs, byID, provider, func(proxy *speedtester.CProxy) *http.Client {
		return speedtester.NewProxyHTTPClient(proxy.Proxy, regionProviderTimeout)
	}, regionNodeConcurrent)
	return writeRegionEventStream(ctx, output, request.IDs, results)
}

func writeRegionEventStream(
	ctx context.Context,
	output io.Writer,
	ids []string,
	results <-chan regionEvent,
) error {
	if ctx == nil {
		return fmt.Errorf("region query context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if output == nil {
		return fmt.Errorf("region event output is nil")
	}
	if results == nil {
		return fmt.Errorf("region event results channel is nil")
	}
	tracker, err := newRegionEventTracker(ids)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(output)
	if _, err := fmt.Fprintf(writer, "@protocol\t%d\n", regionEventProtocolVersion); err != nil {
		return fmt.Errorf("write region protocol header: %w", err)
	}
	if _, err := fmt.Fprintf(writer, "@regions\t%d\n", len(ids)); err != nil {
		return fmt.Errorf("write region count header: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush region headers: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var event regionEvent
		var open bool
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, open = <-results:
		}
		if !open {
			if err := ctx.Err(); err != nil {
				return err
			}
			return tracker.ValidateCompletion()
		}
		if err := tracker.Accept(event); err != nil {
			return err
		}
		if err := writeEvent(writer, "@regionjson", event); err != nil {
			return fmt.Errorf("write region event for %q: %w", event.NodeID, err)
		}
		if err := writer.Flush(); err != nil {
			return fmt.Errorf("flush region event for %q: %w", event.NodeID, err)
		}
	}
}

func queryRegionEvents(
	ctx context.Context,
	ids []string,
	byID map[string]*speedtester.CProxy,
	provider *FallbackRegionProvider,
	clientFactory regionHTTPClientFactory,
	parallel int,
) <-chan regionEvent {
	type queryJob struct {
		id    string
		proxy *speedtester.CProxy
	}
	workers := parallel
	if workers < 1 {
		workers = 1
	}
	if workers > len(ids) {
		workers = len(ids)
	}
	if workers == 0 {
		results := make(chan regionEvent)
		close(results)
		return results
	}
	jobs := make(chan queryJob)
	results := make(chan regionEvent, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				if err := ctx.Err(); err != nil {
					return
				}
				var job queryJob
				var open bool
				select {
				case <-ctx.Done():
					return
				case job, open = <-jobs:
				}
				if !open {
					return
				}
				if err := ctx.Err(); err != nil {
					return
				}
				var event regionEvent
				if job.proxy == nil {
					event = regionEvent{NodeID: job.id, Error: "未知节点 ID"}
				} else if provider == nil || clientFactory == nil {
					event = regionEvent{NodeID: job.id, Error: "地区查询器未正确初始化"}
				} else {
					nodeCtx, cancel := context.WithTimeout(ctx, regionNodeBudget)
					client := clientFactory(job.proxy)
					if client == nil {
						cancel()
						event = regionEvent{NodeID: job.id, Error: "地区查询 HTTP 客户端为空"}
					} else {
						region, queryErr := provider.Lookup(nodeCtx, client)
						cancel()
						if transport, ok := client.Transport.(*http.Transport); ok {
							transport.CloseIdleConnections()
						}
						if err := ctx.Err(); err != nil {
							return
						}
						event = regionEvent{NodeID: job.id}
						if queryErr != nil {
							event.Error = compactRegionError(queryErr)
						} else {
							event.Success = true
							event.CountryCode = region.CountryCode
							event.Country = region.Country
							event.City = region.City
							event.Emoji = region.Emoji
							event.Provider = region.Provider
						}
					}
				}
				if err := ctx.Err(); err != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case results <- event:
				}
			}
		}()
	}
	go func() {
		defer func() {
			close(jobs)
			group.Wait()
			close(results)
		}()
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- queryJob{id: id, proxy: byID[id]}:
			}
		}
	}()
	return results
}

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clash-speedtest.local/speedtest-runner/internal/upstream/speedtester"
)

type mockRegionProvider struct {
	name   string
	lookup func(context.Context, *http.Client) (RegionResult, error)
	calls  atomic.Int32
}

type failOnWriteCall struct {
	calls  int
	failAt int
	output bytes.Buffer
}

func (w *failOnWriteCall) Write(value []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, fmt.Errorf("forced write failure on call %d", w.calls)
	}
	return w.output.Write(value)
}

func (p *mockRegionProvider) Name() string { return p.name }
func (p *mockRegionProvider) Lookup(ctx context.Context, client *http.Client) (RegionResult, error) {
	p.calls.Add(1)
	return p.lookup(ctx, client)
}

func TestHTTPRegionProvidersAndFallbackParsing(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		provider func(string) RegionProvider
		wantCode string
		wantCity string
		wantErr  string
	}{
		{"ipwhois-success", 200, `{"success":true,"country_code":"JP","country":"日本","city":"东京","flag":{"emoji":"🇯🇵"}}`, func(url string) RegionProvider { return ipWhoisRegionProvider{url} }, "JP", "东京", ""},
		{"ipwhois-city-missing", 200, `{"success":true,"country_code":"JP","country":"日本","flag":{"emoji":"🇯🇵"}}`, func(url string) RegionProvider { return ipWhoisRegionProvider{url} }, "JP", "", ""},
		{"freeipapi-success", 200, `{"countryCode":"US","countryName":"United States","cityName":"Los Angeles"}`, func(url string) RegionProvider { return freeIPAPIRegionProvider{url} }, "US", "Los Angeles", ""},
		{"ipsb-success", 200, `{"country_code":"DE","country":"Germany","city":"Frankfurt"}`, func(url string) RegionProvider { return ipSBRegionProvider{url} }, "DE", "Frankfurt", ""},
		{"rate-limit", 429, `{}`, func(url string) RegionProvider { return ipSBRegionProvider{url} }, "", "", "HTTP 429"},
		{"server-error", 503, `{}`, func(url string) RegionProvider { return ipSBRegionProvider{url} }, "", "", "HTTP 503"},
		{"malformed-json", 200, `{`, func(url string) RegionProvider { return ipSBRegionProvider{url} }, "", "", "JSON 错误"},
		{"business-failure", 200, `{"success":false,"message":"limit exceeded"}`, func(url string) RegionProvider { return ipWhoisRegionProvider{url} }, "", "", "业务失败"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			chain := NewFallbackRegionProvider(test.provider(server.URL))
			result, err := chain.Lookup(context.Background(), server.Client())
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.CountryCode != test.wantCode || result.City != test.wantCity {
				t.Fatalf("unexpected result: %#v", result)
			}
			if test.name == "freeipapi-success" && result.Country != "美国" {
				t.Fatalf("fallback country was not localized: %#v", result)
			}
		})
	}
}

func TestEveryRegionProviderHTTPFailureTimeoutAndMalformedJSON(t *testing.T) {
	constructors := []struct {
		name string
		new  func(string) RegionProvider
	}{
		{"ipwhois", func(url string) RegionProvider { return ipWhoisRegionProvider{url} }},
		{"freeipapi", func(url string) RegionProvider { return freeIPAPIRegionProvider{url} }},
		{"ipsb", func(url string) RegionProvider { return ipSBRegionProvider{url} }},
	}
	for _, constructor := range constructors {
		for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusBadGateway} {
			t.Run(constructor.name+"-http-"+http.StatusText(status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
				}))
				defer server.Close()
				_, err := constructor.new(server.URL).Lookup(context.Background(), server.Client())
				if err == nil || !strings.Contains(err.Error(), "HTTP") {
					t.Fatalf("expected HTTP error, got %v", err)
				}
			})
		}
		t.Run(constructor.name+"-malformed", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{`))
			}))
			defer server.Close()
			_, err := constructor.new(server.URL).Lookup(context.Background(), server.Client())
			if err == nil || !strings.Contains(err.Error(), "JSON") {
				t.Fatalf("expected JSON error, got %v", err)
			}
		})
		t.Run(constructor.name+"-timeout", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(100 * time.Millisecond)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			_, err := constructor.new(server.URL).Lookup(ctx, server.Client())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected timeout, got %v", err)
			}
		})
	}
}

func TestFallbackProviderOrderPauseCircuitAndFinalFailure(t *testing.T) {
	limited := &mockRegionProvider{name: "limited", lookup: func(context.Context, *http.Client) (RegionResult, error) {
		return RegionResult{}, &providerError{StatusCode: 429, Message: "HTTP 429"}
	}}
	backup := &mockRegionProvider{name: "backup", lookup: func(context.Context, *http.Client) (RegionResult, error) {
		return RegionResult{CountryCode: "JP", Country: "Japan", City: "Tokyo"}, nil
	}}
	chain := NewFallbackRegionProvider(limited, backup)
	for range 2 {
		result, err := chain.Lookup(context.Background(), http.DefaultClient)
		if err != nil || result.Provider != "backup" {
			t.Fatalf("fallback failed: %#v, %v", result, err)
		}
	}
	if limited.calls.Load() != 1 || backup.calls.Load() != 2 {
		t.Fatalf("429 provider was not paused: limited=%d backup=%d", limited.calls.Load(), backup.calls.Load())
	}

	broken := &mockRegionProvider{name: "broken", lookup: func(context.Context, *http.Client) (RegionResult, error) {
		return RegionResult{}, &providerError{Network: true, Message: "network down"}
	}}
	last := &mockRegionProvider{name: "last", lookup: func(context.Context, *http.Client) (RegionResult, error) {
		return RegionResult{}, errors.New("no region")
	}}
	circuit := NewFallbackRegionProvider(broken, last)
	for range 4 {
		_, _ = circuit.Lookup(context.Background(), http.DefaultClient)
	}
	if broken.calls.Load() != 3 || last.calls.Load() != 4 {
		t.Fatalf("network circuit breaker failed: broken=%d last=%d", broken.calls.Load(), last.calls.Load())
	}
	_, err := circuit.Lookup(context.Background(), http.DefaultClient)
	if err == nil || !strings.Contains(err.Error(), "last") {
		t.Fatalf("final error summary missing provider: %v", err)
	}
}

func TestFallbackProviderCancellationStopsWithoutFanout(t *testing.T) {
	started := make(chan struct{})
	first := &mockRegionProvider{name: "first", lookup: func(ctx context.Context, _ *http.Client) (RegionResult, error) {
		close(started)
		<-ctx.Done()
		return RegionResult{}, ctx.Err()
	}}
	second := &mockRegionProvider{name: "second", lookup: func(context.Context, *http.Client) (RegionResult, error) {
		return RegionResult{CountryCode: "JP", Country: "日本"}, nil
	}}
	chain := NewFallbackRegionProvider(first, second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := chain.Lookup(ctx, http.DefaultClient)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("lookup error = %v, want canceled", err)
	}
	if second.calls.Load() != 0 {
		t.Fatalf("canceled lookup fanned out to backup provider")
	}
}

func TestQueryRegionEventsStableSelectionUnknownAndConcurrency(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	provider := &mockRegionProvider{name: "mock", lookup: func(context.Context, *http.Client) (RegionResult, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		return RegionResult{CountryCode: "JP", Country: "日本", City: "东京"}, nil
	}}
	byID := map[string]*speedtester.CProxy{
		"stable-a": {}, "stable-b": {}, "stable-c": {}, "stable-d": {},
	}
	ids := []string{"stable-a", "unknown", "stable-b", "stable-c", "stable-d"}
	results := queryRegionEvents(context.Background(), ids, byID,
		NewFallbackRegionProvider(provider),
		func(*speedtester.CProxy) *http.Client { return http.DefaultClient }, 4)
	seen := make(map[string]regionEvent)
	for event := range results {
		seen[event.NodeID] = event
	}
	if len(seen) != len(ids) {
		t.Fatalf("event count = %d, want %d", len(seen), len(ids))
	}
	if seen["unknown"].Success || !strings.Contains(seen["unknown"].Error, "未知") {
		t.Fatalf("unknown ID was not isolated: %#v", seen["unknown"])
	}
	for _, id := range []string{"stable-a", "stable-b", "stable-c", "stable-d"} {
		if !seen[id].Success || seen[id].CountryCode != "JP" {
			t.Fatalf("stable ID %s failed: %#v", id, seen[id])
		}
	}
	if maxActive.Load() < 2 || maxActive.Load() > 4 {
		t.Fatalf("unexpected node concurrency: %d", maxActive.Load())
	}
}

func TestRegionRequestIDsAndEventTrackerStrictness(t *testing.T) {
	ids, err := normalizeRegionRequestIDs([]string{"node-a", "node-b"})
	if err != nil || len(ids) != 2 || ids[0] != "node-a" {
		t.Fatalf("valid request IDs were rejected: %v, %v", ids, err)
	}
	for _, invalid := range [][]string{{""}, {" node-a "}, {"node-a", "node-a"}} {
		if _, err := normalizeRegionRequestIDs(invalid); err == nil {
			t.Fatalf("invalid request IDs accepted: %#v", invalid)
		}
	}

	tracker, err := newRegionEventTracker(ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.Accept(regionEvent{
		NodeID: "node-a", Success: true, CountryCode: "JP", Country: "日本", Provider: "mock",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Accept(regionEvent{NodeID: "node-b", Error: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ValidateCompletion(); err != nil {
		t.Fatal(err)
	}

	unknown, _ := newRegionEventTracker([]string{"node-a"})
	if err := unknown.Accept(regionEvent{NodeID: "other", Error: "unknown"}); err == nil {
		t.Fatal("unknown region event ID must be rejected")
	}
	duplicate, _ := newRegionEventTracker([]string{"node-a"})
	event := regionEvent{NodeID: "node-a", Error: "timeout"}
	if err := duplicate.Accept(event); err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Accept(event); err == nil {
		t.Fatal("duplicate region event must be rejected")
	}
	incomplete, _ := newRegionEventTracker([]string{"node-a", "node-b"})
	if err := incomplete.Accept(event); err != nil {
		t.Fatal(err)
	}
	if err := incomplete.ValidateCompletion(); err == nil {
		t.Fatal("incomplete region event stream must be rejected")
	}

	for name, invalid := range map[string]regionEvent{
		"lowercase country":        {NodeID: "node-a", Success: true, CountryCode: "jp", Country: "日本", Provider: "mock"},
		"country with whitespace":  {NodeID: "node-a", Success: true, CountryCode: "JP", Country: " 日本 ", Provider: "mock"},
		"provider with whitespace": {NodeID: "node-a", Success: true, CountryCode: "JP", Country: "日本", Provider: " mock "},
		"success with error":       {NodeID: "node-a", Success: true, CountryCode: "JP", Country: "日本", Provider: "mock", Error: "bad"},
		"failure error whitespace": {NodeID: "node-a", Error: " timeout "},
		"failure with country":     {NodeID: "node-a", CountryCode: "JP", Error: "timeout"},
	} {
		strict, strictErr := newRegionEventTracker([]string{"node-a"})
		if strictErr != nil {
			t.Fatal(strictErr)
		}
		if err := strict.Accept(invalid); err == nil {
			t.Fatalf("%s event was accepted: %#v", name, invalid)
		}
	}
}

func TestDecodeRegionQueryRequestStrictness(t *testing.T) {
	request, err := decodeRegionQueryRequest([]byte(`{"ids":["node-a","node-b"]}`))
	if err != nil || len(request.IDs) != 2 || request.IDs[1] != "node-b" {
		t.Fatalf("valid request was rejected: %#v, %v", request, err)
	}
	invalid := map[string]string{
		"empty":         `{"ids":[]}`,
		"null":          `null`,
		"unknown field": `{"ids":["node-a"],"extra":true}`,
		"trailing JSON": `{"ids":["node-a"]} {}`,
		"blank ID":      `{"ids":[""]}`,
		"unnormalized":  `{"ids":[" node-a "]}`,
		"duplicate":     `{"ids":["node-a","node-a"]}`,
	}
	for name, body := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRegionQueryRequest([]byte(body)); err == nil {
				t.Fatalf("invalid request was accepted: %s", body)
			}
		})
	}
}

func TestWriteRegionEventStreamStrictnessAndWriterErrors(t *testing.T) {
	events := make(chan regionEvent, 2)
	events <- regionEvent{
		NodeID: "node-a", Success: true, CountryCode: "JP", Country: "日本", Provider: "mock",
	}
	events <- regionEvent{NodeID: "node-b", Error: "timeout"}
	close(events)
	var output bytes.Buffer
	if err := writeRegionEventStream(context.Background(), &output,
		[]string{"node-a", "node-b"}, events); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 || lines[0] != "@protocol\t2" || lines[1] != "@regions\t2" ||
		!strings.HasPrefix(lines[2], "@regionjson\t") || !strings.HasPrefix(lines[3], "@regionjson\t") {
		t.Fatalf("unexpected region stream: %q", output.String())
	}
	for index, line := range lines[2:] {
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			t.Fatalf("malformed event line: %q", line)
		}
		body, err := base64.RawStdEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		if _, ok := envelope["node_id"].(string); !ok {
			t.Fatalf("event %d has no string node_id: %#v", index, envelope)
		}
		if _, ok := envelope["success"].(bool); !ok {
			t.Fatalf("event %d has no bool success: %#v", index, envelope)
		}
	}

	incomplete := make(chan regionEvent, 1)
	incomplete <- regionEvent{NodeID: "node-a", Error: "timeout"}
	close(incomplete)
	if err := writeRegionEventStream(context.Background(), &bytes.Buffer{},
		[]string{"node-a", "node-b"}, incomplete); err == nil {
		t.Fatal("incomplete region stream was accepted")
	}

	duplicate := make(chan regionEvent, 2)
	duplicate <- regionEvent{NodeID: "node-a", Error: "timeout"}
	duplicate <- regionEvent{NodeID: "node-a", Error: "timeout again"}
	close(duplicate)
	if err := writeRegionEventStream(context.Background(), &bytes.Buffer{},
		[]string{"node-a", "node-b"}, duplicate); err == nil {
		t.Fatal("duplicate region stream was accepted")
	}

	unknown := make(chan regionEvent, 1)
	unknown <- regionEvent{NodeID: "other", Error: "unknown"}
	close(unknown)
	if err := writeRegionEventStream(context.Background(), &bytes.Buffer{},
		[]string{"node-a"}, unknown); err == nil {
		t.Fatal("unknown region stream ID was accepted")
	}

	closed := make(chan regionEvent)
	close(closed)
	if err := writeRegionEventStream(context.Background(), alwaysFailWriter{},
		[]string{"node-a"}, closed); err == nil {
		t.Fatal("region stream writer failure was ignored")
	}
	stagedFailure := &failOnWriteCall{failAt: 2}
	oneEvent := make(chan regionEvent, 1)
	oneEvent <- regionEvent{NodeID: "node-a", Error: "timeout"}
	close(oneEvent)
	if err := writeRegionEventStream(context.Background(), stagedFailure,
		[]string{"node-a"}, oneEvent); err == nil || stagedFailure.calls != 2 {
		t.Fatalf("region event-stage writer failure was ignored: calls=%d error=%v",
			stagedFailure.calls, err)
	}
	if err := writeRegionEventStream(context.Background(), &bytes.Buffer{},
		[]string{"node-a"}, nil); err == nil {
		t.Fatal("nil region results channel was accepted")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeRegionEventStream(canceled, &bytes.Buffer{}, []string{"node-a"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled region stream error = %v, want context.Canceled", err)
	}
	if err := runRegionQueryContext(canceled, "missing.yaml", "missing.json", &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled region query error = %v, want context.Canceled", err)
	}
}

func TestQueryRegionEventsCancellationStopsScheduling(t *testing.T) {
	const parallel = 4
	ids := make([]string, 100)
	byID := make(map[string]*speedtester.CProxy, len(ids))
	for index := range ids {
		ids[index] = fmt.Sprintf("node-%03d", index)
		byID[ids[index]] = &speedtester.CProxy{}
	}
	started := make(chan struct{})
	var startedOnce sync.Once
	provider := &mockRegionProvider{name: "blocking", lookup: func(ctx context.Context, _ *http.Client) (RegionResult, error) {
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return RegionResult{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	results := queryRegionEvents(ctx, ids, byID, NewFallbackRegionProvider(provider),
		func(*speedtester.CProxy) *http.Client { return http.DefaultClient }, parallel)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("region worker did not start")
	}
	cancel()
	done := make(chan int, 1)
	go func() {
		count := 0
		for range results {
			count++
		}
		done <- count
	}()
	select {
	case count := <-done:
		if count != 0 {
			t.Fatalf("canceled query emitted %d events", count)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled region query did not close promptly")
	}
	if calls := provider.calls.Load(); calls < 1 || calls > parallel {
		t.Fatalf("canceled query scheduled %d providers, want 1..%d", calls, parallel)
	}
}

func TestCountryAndCityFormattingInputs(t *testing.T) {
	result := normalizeRegionResult(RegionResult{CountryCode: "jp", Country: "Japan", City: ""}, "IP.SB")
	if result.Country != "日本" || result.Emoji != "🇯🇵" || result.City != "" {
		t.Fatalf("unexpected normalized region: %#v", result)
	}
	invalid := normalizeRegionResult(RegionResult{CountryCode: "1!", Country: "invalid", Emoji: "x"}, "mock")
	if invalid.CountryCode != "" || invalid.Emoji != "" {
		t.Fatalf("invalid country code survived normalization: %#v", invalid)
	}
}

func TestFallbackSuccessDoesNotCallMoreProviders(t *testing.T) {
	var orderMu sync.Mutex
	var order []string
	makeProvider := func(name string, success bool) *mockRegionProvider {
		return &mockRegionProvider{name: name, lookup: func(context.Context, *http.Client) (RegionResult, error) {
			orderMu.Lock()
			order = append(order, name)
			orderMu.Unlock()
			if success {
				return RegionResult{CountryCode: "CN", Country: "中国"}, nil
			}
			return RegionResult{}, errors.New("failed")
		}}
	}
	chain := NewFallbackRegionProvider(makeProvider("one", false), makeProvider("two", true), makeProvider("three", true))
	if _, err := chain.Lookup(context.Background(), http.DefaultClient); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "one,two" {
		t.Fatalf("providers were not called sequentially: %v", order)
	}
}

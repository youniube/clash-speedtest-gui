package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metacubex/mihomo/common/convert"
	"gopkg.in/yaml.v3"
)

const (
	defaultUserAgent = "mihomo/1.19.27"
	maxHTTPAttempts  = 3
)

type rawConfig struct {
	Providers map[string]map[string]any `yaml:"proxy-providers,omitempty"`
	Proxies   []map[string]any          `yaml:"proxies,omitempty"`
}

type parseResult struct {
	body      []byte
	format    string
	proxies   int
	providers int
}

func main() {
	input := flag.String("input", "", "subscription URL or local file")
	output := flag.String("output", "", "output Clash YAML path")
	userAgent := flag.String("ua", "", "User-Agent for HTTP requests")
	timeout := flag.Duration("timeout", 30*time.Second, "HTTP timeout")
	flag.Parse()

	if strings.TrimSpace(*input) == "" || strings.TrimSpace(*output) == "" {
		fail("input and output are required")
	}

	ua := strings.TrimSpace(*userAgent)
	if ua == "" {
		ua = defaultUserAgent
	}

	result, err := loadAndParse(strings.TrimSpace(*input), ua, *timeout)
	if err != nil {
		fail(err.Error())
	}

	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fail("create output directory failed")
	}
	if err := os.WriteFile(*output, result.body, 0o600); err != nil {
		fail("write output failed")
	}

	fmt.Printf("format=%s;proxies=%d;providers=%d\n", result.format, result.proxies, result.providers)
}

func loadAndParse(input, userAgent string, timeout time.Duration) (*parseResult, error) {
	body, err := readInput(input, userAgent, timeout)
	if err == nil {
		if result, parseErr := parseSubscription(body); parseErr == nil {
			return result, nil
		}
	}

	fallback := withoutMetaFlag(input)
	if fallback != input {
		fallbackBody, fallbackErr := readInput(fallback, userAgent, timeout)
		if fallbackErr == nil {
			if result, parseErr := parseSubscription(fallbackBody); parseErr == nil {
				return result, nil
			}
		}
	}

	if err != nil {
		return nil, fmt.Errorf("subscription request failed: %w", err)
	}
	return nil, fmt.Errorf("unsupported or empty subscription format")
}

func readInput(input, userAgent string, timeout time.Duration) ([]byte, error) {
	if isInlineNode(input) {
		return []byte(input), nil
	}

	if !strings.HasPrefix(strings.ToLower(input), "http://") &&
		!strings.HasPrefix(strings.ToLower(input), "https://") {
		return os.ReadFile(input)
	}

	return readHTTPInput(input, userAgent, timeout)
}

func readHTTPInput(input, userAgent string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := &http.Client{}
	var lastErr error
	for attempt := 1; attempt <= maxHTTPAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, input, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := client.Do(req)
		retryable := err != nil
		if err != nil {
			lastErr = err
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("server returned HTTP %d", resp.StatusCode)
			retryable = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		} else {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
			_ = resp.Body.Close()
			if readErr == nil {
				return body, nil
			}
			lastErr = readErr
			retryable = true
		}

		if !retryable || attempt == maxHTTPAttempts {
			return nil, lastErr
		}
		if err := waitForRetry(ctx, time.Duration(attempt)*200*time.Millisecond); err != nil {
			return nil, fmt.Errorf("%w: %v", err, lastErr)
		}
	}
	return nil, lastErr
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isInlineNode(input string) bool {
	schemeEnd := strings.Index(input, "://")
	if schemeEnd <= 0 {
		return false
	}

	switch strings.ToLower(input[:schemeEnd]) {
	case "ss", "ssr", "vmess", "vless", "trojan",
		"hysteria", "hysteria2", "hy2", "tuic", "anytls",
		"socks", "socks5", "socks5h":
		return true
	default:
		return false
	}
}

func parseSubscription(body []byte) (*parseResult, error) {
	var config rawConfig
	if err := yaml.Unmarshal(body, &config); err == nil &&
		(len(config.Proxies) > 0 || len(config.Providers) > 0) {
		normalized, err := yaml.Marshal(&config)
		if err != nil {
			return nil, err
		}
		return &parseResult{
			body:      normalized,
			format:    "clash-yaml",
			proxies:   len(config.Proxies),
			providers: len(config.Providers),
		}, nil
	}

	proxies, err := convert.ConvertsV2Ray(body)
	if err != nil || len(proxies) == 0 {
		return nil, fmt.Errorf("unsupported or empty subscription format")
	}

	converted := &rawConfig{
		Providers: map[string]map[string]any{},
		Proxies:   proxies,
	}
	normalized, err := yaml.Marshal(converted)
	if err != nil {
		return nil, err
	}
	return &parseResult{
		body:    normalized,
		format:  "base64-or-uri-list",
		proxies: len(proxies),
	}, nil
}

func withoutMetaFlag(input string) string {
	parsed, err := url.Parse(input)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return input
	}
	query := parsed.Query()
	if !strings.EqualFold(query.Get("flag"), "meta") {
		return input
	}
	query.Del("flag")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

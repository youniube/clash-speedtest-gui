package main

import (
	"context"
	"errors"
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
	defaultUserAgent  = "mihomo/1.19.27"
	maxHTTPAttempts   = 3
	maxHTTPInputBytes = 32 * 1024 * 1024
	maxHTTPInputMiB   = maxHTTPInputBytes / (1024 * 1024)
)

var (
	errInvalidSubscriptionURL = errors.New("invalid subscription URL")
	errHTTPNetwork            = errors.New("subscription network request failed")
	errHTTPRead               = errors.New("subscription response read failed")
	errHTTPTimeout            = errors.New("subscription request timed out")
	errHTTPInputTooLarge      = fmt.Errorf("subscription response exceeds %d MiB limit", maxHTTPInputMiB)
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
	var parseErr error
	body, err := readInput(input, userAgent, timeout)
	if err == nil {
		var result *parseResult
		result, parseErr = parseSubscriptionForInput(body, input)
		if parseErr == nil {
			return result, nil
		}
	}

	fallback := withoutMetaFlag(input)
	if fallback != input {
		fallbackBody, fallbackErr := readInput(fallback, userAgent, timeout)
		if fallbackErr == nil {
			if result, parseErr := parseSubscriptionForInput(fallbackBody, fallback); parseErr == nil {
				return result, nil
			}
		}
	}

	if err != nil {
		return nil, fmt.Errorf("subscription request failed: %w", err)
	}
	if parseErr != nil {
		return nil, parseErr
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
			return nil, errInvalidSubscriptionURL
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := client.Do(req)
		retryable := err != nil
		if err != nil {
			lastErr = sanitizedHTTPError(ctx, errHTTPNetwork)
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("server returned HTTP %d", resp.StatusCode)
			retryable = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		} else {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHTTPInputBytes+1))
			_ = resp.Body.Close()
			if readErr != nil {
				lastErr = sanitizedHTTPError(ctx, errHTTPRead)
				retryable = true
			} else if len(body) > maxHTTPInputBytes {
				return nil, errHTTPInputTooLarge
			} else {
				return body, nil
			}
		}

		if !retryable || attempt == maxHTTPAttempts {
			return nil, lastErr
		}
		if err := waitForRetry(ctx, time.Duration(attempt)*200*time.Millisecond); err != nil {
			return nil, sanitizedHTTPError(ctx, lastErr)
		}
	}
	return nil, lastErr
}

func sanitizedHTTPError(ctx context.Context, fallback error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errHTTPTimeout
	}
	return fallback
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
	return parseSubscriptionForInput(body, "")
}

func parseSubscriptionForInput(body []byte, input string) (*parseResult, error) {
	var config rawConfig
	if err := yaml.Unmarshal(body, &config); err == nil &&
		(len(config.Proxies) > 0 || len(config.Providers) > 0) {
		normalizeLocalProviderPaths(&config, input)
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

	proxies, err := convertStrictURIList(body)
	if err != nil {
		return nil, err
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

func convertStrictURIList(body []byte) ([]map[string]any, error) {
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
				"subscription URI line %d is invalid or unsupported", index+1)
		}
	}
	if nonEmptyLines == 0 {
		return nil, fmt.Errorf("unsupported or empty subscription format")
	}

	// Convert the complete list after validating every physical line so
	// Mihomo can still apply its stable duplicate-name numbering across lines.
	proxies, err := convert.ConvertsV2Ray(decoded)
	if err != nil || len(proxies) != nonEmptyLines {
		return nil, fmt.Errorf("subscription URI list could not be converted completely")
	}
	return proxies, nil
}

func normalizeLocalProviderPaths(config *rawConfig, input string) {
	if config == nil || strings.TrimSpace(input) == "" || isInlineNode(input) || isHTTPInput(input) {
		return
	}
	absoluteInput, err := filepath.Abs(input)
	if err != nil {
		return
	}
	baseDirectory := filepath.Dir(absoluteInput)
	for _, providerConfig := range config.Providers {
		if providerURL, ok := providerConfig["url"].(string); ok && strings.TrimSpace(providerURL) != "" {
			continue
		}
		providerPath, ok := providerConfig["path"].(string)
		if !ok || strings.TrimSpace(providerPath) == "" || filepath.IsAbs(providerPath) {
			continue
		}
		providerConfig["path"] = filepath.Clean(filepath.Join(baseDirectory, providerPath))
	}
}

func isHTTPInput(input string) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
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

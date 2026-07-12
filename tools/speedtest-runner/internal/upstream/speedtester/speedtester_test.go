package speedtester

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTransferSummaryAdd(t *testing.T) {
	summary := newTransferSummary()
	summary.add(nil)

	errorMessage := "download request to https://example.com/__down?bytes=1 failed: boom"
	summary.add(&downloadResult{error: errorMessage})
	if summary.successCount != 0 {
		t.Fatalf("expected successCount to remain 0, got %d", summary.successCount)
	}
	if len(summary.errors) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(summary.errors))
	}
	if summary.errors[0] != errorMessage {
		t.Fatalf("expected error message %q, got %q", errorMessage, summary.errors[0])
	}

	summary.add(&downloadResult{error: errorMessage})
	if len(summary.errors) != 1 {
		t.Fatalf("expected duplicate errors to be deduplicated, got %d", len(summary.errors))
	}

	summary.add(&downloadResult{bytes: 100, duration: time.Second})
	summary.add(&downloadResult{bytes: 50, duration: 2 * time.Second})

	if summary.successCount != 2 {
		t.Fatalf("expected successCount to be 2, got %d", summary.successCount)
	}
	if summary.totalBytes != 150 {
		t.Fatalf("expected totalBytes to be 150, got %d", summary.totalBytes)
	}
	size, duration, speed, transferError := applyTransferSummary(summary, 2*time.Second)
	if transferError == "" {
		t.Fatal("an explicit connection error must still fail the transfer")
	}
	if size != 150 || duration != 2*time.Second || speed != 0 {
		t.Fatalf("unexpected failed transfer summary: size=%v duration=%v speed=%v", size, duration, speed)
	}
}

func TestTransferSummaryUsesBatchWallClockDuration(t *testing.T) {
	summary := newTransferSummary()
	summary.add(&downloadResult{bytes: 100, duration: time.Second})
	summary.add(&downloadResult{bytes: 100, duration: 4 * time.Second})

	size, duration, speed, transferError := applyTransferSummary(summary, 4*time.Second)
	if transferError != "" {
		t.Fatalf("unexpected transfer error: %s", transferError)
	}
	if size != 200 || duration != 4*time.Second {
		t.Fatalf("unexpected transfer totals: size=%v duration=%v", size, duration)
	}
	if speed != 50 {
		t.Fatalf("speed must use batch wall-clock duration: got %v, want 50", speed)
	}
}

func TestLatencyUsesRequestTimeoutInsteadOfFilterThreshold(t *testing.T) {
	tester, err := New(&Config{
		ServerURL:  "https://example.com/file.bin",
		Mode:       SpeedModeFast,
		Timeout:    3 * time.Second,
		MaxLatency: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tester.latencyRequestTimeout() != 3*time.Second {
		t.Fatalf("latency timeout=%v, want 3s", tester.latencyRequestTimeout())
	}
	if tester.config.MaxLatency != 0 {
		t.Fatal("latency filter threshold must remain independent from request timeout")
	}
}

func TestConsumeDownloadResponseEnforcesRangeAndSize(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		contentRange string
		body         string
		wantBytes    int64
		wantError    string
	}{
		{name: "valid partial response", status: http.StatusPartialContent,
			contentRange: "bytes 0-3/10", body: "abcd", wantBytes: 4},
		{name: "full response is capped", status: http.StatusOK,
			body: "abcdefghij", wantBytes: 4},
		{name: "short full response", status: http.StatusOK,
			body: "ab", wantError: "shorter than requested"},
		{name: "missing content range", status: http.StatusPartialContent,
			body: "abcd", wantError: "Content-Range"},
		{name: "wrong content range", status: http.StatusPartialContent,
			contentRange: "bytes 1-4/10", body: "abcd", wantError: "Content-Range"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: test.status,
				Status:     http.StatusText(test.status),
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(test.body)),
			}
			if test.contentRange != "" {
				response.Header.Set("Content-Range", test.contentRange)
			}
			got, err := consumeDownloadResponse(response, 4)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected error containing %q, got %v", test.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.wantBytes {
				t.Fatalf("read bytes=%d, want %d", got, test.wantBytes)
			}
		})
	}
}

func TestResultFormatErrors(t *testing.T) {
	result := &Result{}
	if result.FormatDownloadError() != "N/A" {
		t.Fatalf("expected empty download error to format as N/A, got %q", result.FormatDownloadError())
	}
	if result.FormatUploadError() != "N/A" {
		t.Fatalf("expected empty upload error to format as N/A, got %q", result.FormatUploadError())
	}

	result.DownloadError = "download failed: timeout"
	result.UploadError = "upload failed: status 500"
	if result.FormatDownloadError() != result.DownloadError {
		t.Fatalf("expected download error to pass through, got %q", result.FormatDownloadError())
	}
	if result.FormatUploadError() != result.UploadError {
		t.Fatalf("expected upload error to pass through, got %q", result.FormatUploadError())
	}

	result.DownloadSpeed = 1024
	result.UploadSpeed = 2048
	if result.FormatDownloadSpeed() != result.DownloadError {
		t.Fatalf("expected download speed to prefer error string, got %q", result.FormatDownloadSpeed())
	}
	if result.FormatUploadSpeed() != result.UploadError {
		t.Fatalf("expected upload speed to prefer error string, got %q", result.FormatUploadSpeed())
	}
	if result.FormatDownloadSpeedValue() == result.DownloadError {
		t.Fatalf("expected download speed value to ignore error string")
	}
	if result.FormatUploadSpeedValue() == result.UploadError {
		t.Fatalf("expected upload speed value to ignore error string")
	}
}

func TestFetchHTTPConfigRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "subscription unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	tester, err := New(&Config{
		ServerURL: "https://example.com/file.bin",
		Timeout:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = tester.fetchHTTPConfig(server.URL)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("expected status in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "subscription unavailable") {
		t.Fatalf("expected response detail in error, got %v", err)
	}
}

func TestHTTPURLDetection(t *testing.T) {
	if !isHTTPURL(" HTTPS://example.com/sub ") {
		t.Fatal("expected https URL to be detected")
	}
	if isHTTPURL("http-file.yaml") {
		t.Fatal("local file names starting with http must not be treated as URLs")
	}
}

func TestStringMapValueRequiresString(t *testing.T) {
	if value, ok := stringMapValue(map[string]any{"server": "example.com"}, "server"); !ok || value != "example.com" {
		t.Fatalf("expected string value, got %q %v", value, ok)
	}
	if _, ok := stringMapValue(map[string]any{"server": 1234}, "server"); ok {
		t.Fatal("non-string value must not be accepted")
	}
	if _, ok := stringMapValue(nil, "server"); ok {
		t.Fatal("nil map must not return a value")
	}
}

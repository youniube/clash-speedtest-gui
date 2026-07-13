package speedtester

import (
	"strings"
	"testing"
)

func TestResolveServerTarget(t *testing.T) {
	t.Run("download server without path", func(t *testing.T) {
		target, err := resolveServerTarget("https://example.com")
		if err != nil {
			t.Fatalf("resolveServerTarget failed: %v", err)
		}
		if target.mode != serverModeDownloadServer {
			t.Fatalf("expected download server mode, got %v", target.mode)
		}
		if target.baseURL != "https://example.com" {
			t.Fatalf("expected baseURL to be trimmed, got %q", target.baseURL)
		}
	})

	t.Run("download server with trailing slash", func(t *testing.T) {
		target, err := resolveServerTarget("https://example.com/")
		if err != nil {
			t.Fatalf("resolveServerTarget failed: %v", err)
		}
		if target.mode != serverModeDownloadServer {
			t.Fatalf("expected download server mode, got %v", target.mode)
		}
		if target.baseURL != "https://example.com" {
			t.Fatalf("expected baseURL to be trimmed, got %q", target.baseURL)
		}
	})

	t.Run("direct download with path", func(t *testing.T) {
		target, err := resolveServerTarget("https://example.com/file.bin")
		if err != nil {
			t.Fatalf("resolveServerTarget failed: %v", err)
		}
		if target.mode != serverModeDirectDownload {
			t.Fatalf("expected direct download mode, got %v", target.mode)
		}
		if target.downloadURL != "https://example.com/file.bin" {
			t.Fatalf("expected downloadURL to be preserved, got %q", target.downloadURL)
		}
	})

	t.Run("direct download with query", func(t *testing.T) {
		target, err := resolveServerTarget("https://example.com?bytes=1024")
		if err != nil {
			t.Fatalf("resolveServerTarget failed: %v", err)
		}
		if target.mode != serverModeDirectDownload {
			t.Fatalf("expected direct download mode, got %v", target.mode)
		}
		if target.downloadURL != "https://example.com?bytes=1024" {
			t.Fatalf("expected downloadURL to be preserved, got %q", target.downloadURL)
		}
	})

	t.Run("invalid scheme", func(t *testing.T) {
		_, err := resolveServerTarget("ftp://example.com")
		if err == nil {
			t.Fatal("expected error for invalid scheme")
		}
		if !strings.Contains(err.Error(), "ftp://example.com") {
			t.Fatalf("expected error to include server url context, got %v", err)
		}
	})
}

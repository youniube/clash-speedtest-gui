package output

import (
	"testing"
	"time"

	"clash-speedtest.local/speedtest-runner/internal/upstream/speedtester"
)

func TestHeadersUseExplicitHTTPMetrics(t *testing.T) {
	fast := GetHeaders(speedtester.SpeedModeFast)
	if len(fast) != 4 || fast[3] != "HTTP 延迟" {
		t.Fatalf("unexpected fast headers: %v", fast)
	}
	download := GetHeaders(speedtester.SpeedModeDownload)
	want := []string{"序号", "节点名称", "类型", "HTTP 延迟", "抖动", "HTTP 探测失败率", "下载速度"}
	if len(download) != len(want) {
		t.Fatalf("download header count=%d, want %d: %v", len(download), len(want), download)
	}
	for index := range want {
		if download[index] != want[index] {
			t.Fatalf("download header[%d]=%q, want %q", index, download[index], want[index])
		}
	}
}

func TestFormatRowAndSort(t *testing.T) {
	results := []*speedtester.Result{
		{ProxyName: "slow", ProxyType: "VLESS", Latency: 30 * time.Millisecond,
			Jitter: 2 * time.Millisecond, HTTPProbeFailurePercent: 20,
			DownloadSpeed: 3 * 1024 * 1024},
		{ProxyName: "fast", ProxyType: "Trojan", Latency: 10 * time.Millisecond,
			Jitter: time.Millisecond, HTTPProbeFailurePercent: 0,
			DownloadSpeed: 8 * 1024 * 1024},
	}
	downloadSorted := SortResults(append([]*speedtester.Result(nil), results...), speedtester.SpeedModeDownload)
	if downloadSorted[0].ProxyName != "fast" {
		t.Fatalf("download sort order=%v", downloadSorted)
	}
	row := FormatRow(downloadSorted[0], speedtester.SpeedModeDownload, 0)
	if len(row) != 7 || row[0] != "1." || row[3] != "10ms" || row[5] != "0.0%" || row[6] != "8.00MB/s" {
		t.Fatalf("unexpected download row: %v", row)
	}

	fastSorted := SortResults(append([]*speedtester.Result(nil), results...), speedtester.SpeedModeFast)
	if fastSorted[0].ProxyName != "fast" || len(FormatRow(fastSorted[0], speedtester.SpeedModeFast, 0)) != 4 {
		t.Fatalf("unexpected fast result ordering or row")
	}
}

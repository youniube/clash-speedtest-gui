package output

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"clash-speedtest.local/speedtest-runner/internal/upstream/speedtester"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestTSVWriterStreamsPureDownloadRows(t *testing.T) {
	var output bytes.Buffer
	writer, err := NewTSVWriter(&output, speedtester.SpeedModeDownload)
	if err != nil {
		t.Fatal(err)
	}
	result := &speedtester.Result{
		ProxyName: "node", ProxyType: "VLESS", Latency: 25 * time.Millisecond,
		Jitter: 3 * time.Millisecond, HTTPProbeFailurePercent: 20,
		DownloadSpeed: 5 * 1024 * 1024,
	}
	if err := writer.WriteRow(result, 0); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.HasPrefix(text, "序号\t节点名称\t类型\tHTTP 延迟\t抖动\tHTTP 探测失败率\t下载速度\n") {
		t.Fatalf("unexpected header: %q", text)
	}
	if !strings.Contains(text, "1.\tnode\tVLESS\t25ms\t3ms\t20.0%\t5.00MB/s\n") {
		t.Fatalf("unexpected row: %q", text)
	}
}

func TestTSVWriterErrors(t *testing.T) {
	if _, err := NewTSVWriter(failingWriter{}, speedtester.SpeedModeFast); err == nil {
		t.Fatal("expected header write failure")
	}
	var output bytes.Buffer
	writer, err := NewTSVWriter(&output, speedtester.SpeedModeFast)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteRow(nil, 0); err == nil {
		t.Fatal("expected nil result failure")
	}
}

package output

import (
	"fmt"
	"sort"

	"clash-speedtest.local/speedtest-runner/internal/upstream/speedtester"
)

// GetHeaders returns table headers based on speed mode.
// fast: ID, Name, Type, Latency
// download: ID, Name, Type, HTTP Latency, Jitter, HTTP Probe Failure Rate, Download Speed
func GetHeaders(mode speedtester.SpeedMode) []string {
	if mode.IsFast() {
		return []string{
			"序号",
			"节点名称",
			"类型",
			"HTTP 延迟",
		}
	}
	headers := []string{
		"序号",
		"节点名称",
		"类型",
		"HTTP 延迟",
		"抖动",
		"HTTP 探测失败率",
		"下载速度",
	}
	return headers
}

// FormatRow formats a single result row without ANSI colors.
// Returns plain text strings using speedtester.Result's Format* methods.
func FormatRow(result *speedtester.Result, mode speedtester.SpeedMode, index int) []string {
	idStr := fmt.Sprintf("%d.", index+1)

	if mode.IsFast() {
		return []string{
			idStr,
			result.ProxyName,
			result.ProxyType,
			result.FormatLatency(),
		}
	}
	row := []string{
		idStr,
		result.ProxyName,
		result.ProxyType,
		result.FormatLatency(),
		result.FormatJitter(),
		result.FormatHTTPProbeFailure(),
		result.FormatDownloadSpeed(),
	}
	return row
}

// SortResults sorts results based on speed mode.
// fast: latency ascending (lower is better)
// download: download speed descending (higher is better)
func SortResults(results []*speedtester.Result, mode speedtester.SpeedMode) []*speedtester.Result {
	if mode.IsFast() {
		sort.Slice(results, func(i, j int) bool {
			return results[i].Latency < results[j].Latency
		})
	} else {
		sort.Slice(results, func(i, j int) bool {
			return results[i].DownloadSpeed > results[j].DownloadSpeed
		})
	}
	return results
}

package speedtester

import (
	"fmt"
	"strings"
)

type SpeedMode string

const (
	SpeedModeFast     SpeedMode = "fast"
	SpeedModeDownload SpeedMode = "download"
)

func ParseSpeedMode(value string) (SpeedMode, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch SpeedMode(normalized) {
	case SpeedModeFast:
		return SpeedModeFast, nil
	case SpeedModeDownload:
		return SpeedModeDownload, nil
	default:
		return "", fmt.Errorf("unsupported speed mode %q", value)
	}
}

func (m SpeedMode) IsFast() bool {
	return m == SpeedModeFast
}

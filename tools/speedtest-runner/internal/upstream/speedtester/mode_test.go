package speedtester

import "testing"

func TestParseSpeedMode(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  SpeedMode
		wantError bool
	}{
		{
			name:     "fast",
			input:    "fast",
			expected: SpeedModeFast,
		},
		{
			name:     "download",
			input:    "download",
			expected: SpeedModeDownload,
		},
		{
			name:      "removed full mode",
			input:     "full",
			wantError: true,
		},
		{
			name:      "invalid",
			input:     "slow",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, err := ParseSpeedMode(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSpeedMode(%q) failed: %v", tt.input, err)
			}
			if mode != tt.expected {
				t.Fatalf("expected mode %s, got %s", tt.expected, mode)
			}
		})
	}
}

func TestSpeedModeHelpers(t *testing.T) {
	if !SpeedModeFast.IsFast() {
		t.Fatalf("expected fast mode to be fast")
	}
	if SpeedModeDownload.IsFast() {
		t.Fatalf("expected download mode to be non-fast")
	}
}

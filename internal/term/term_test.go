package term

import (
	"bytes"
	"testing"
)

func TestNoColor(t *testing.T) {
	tests := []struct {
		name            string
		noColor, cliclr string
		flag            bool
		want            bool
	}{
		{"nothing set", "", "", false, false},
		{"NO_COLOR", "1", "", false, true},
		{"CLICOLOR=0", "", "0", false, true},
		{"CLICOLOR=1", "", "1", false, false},
		{"--no-color", "", "", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.noColor)
			t.Setenv("CLICOLOR", tt.cliclr)
			SetNoColor(tt.flag)
			defer SetNoColor(false)
			if got := NoColor(); got != tt.want {
				t.Errorf("NoColor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	var buf bytes.Buffer
	if IsTerminal(&buf) {
		t.Error("a buffer is not a terminal")
	}
	if !IsTerminal(Forced{Writer: &buf}) {
		t.Error("Forced is treated as a terminal")
	}
	t.Setenv("NO_COLOR", "1")
	if Color(Forced{Writer: &buf}) {
		t.Error("NO_COLOR must switch color off even on a terminal")
	}
}

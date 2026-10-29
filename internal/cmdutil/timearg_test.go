package cmdutil

import (
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
)

func TestParseTimeArg(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"2026-10-01T09:30:00Z":      time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC),
		"2026-10-01T11:30:00+02:00": time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC),
		"24h":                       now.Add(-24 * time.Hour),
		"90m":                       now.Add(-90 * time.Minute),
		"7d":                        now.AddDate(0, 0, -7),
		"2026-10-01":                time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local).UTC(),
	} {
		got, err := ParseTimeArg("since", in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "yesterday", "-5h", "-2d", "2026-13-01"} {
		if _, err := ParseTimeArg("since", in, now); !clierr.IsUsage(err) {
			t.Errorf("%q: want a usage error, got %v", in, err)
		}
	}
}

package cmdutil

import (
	"strings"
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

func TestParseFutureTimeArg(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.FixedZone("CET", 3600))
	for in, want := range map[string]time.Time{
		"now":                       now.UTC(),
		" now ":                     now.UTC(),
		"+2h":                       now.Add(2 * time.Hour).UTC(),
		"+90m":                      now.Add(90 * time.Minute).UTC(),
		"+1h30m":                    now.Add(90 * time.Minute).UTC(),
		"+7d":                       now.Add(7 * 24 * time.Hour).UTC(),
		"2026-10-29T09:30:00Z":      time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC),
		"2026-10-29T11:30:00+02:00": time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC),
	} {
		got, err := ParseFutureTimeArg("start", in, now)
		if err != nil || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "soon", "-2h", "+", "+0h", "+-2h", "+soon", "2026-10-29", "+-1d", "+0d"} {
		if _, err := ParseFutureTimeArg("start", in, now); !clierr.IsUsage(err) {
			t.Errorf("%q: want a usage error, got %v", in, err)
		}
	}
}

// ParseTimeArg reads a bare span as an age, so here it is refused and the
// message shows the spelling that works.
func TestParseFutureTimeArgRefusesABareSpan(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, in := range []string{"2h", "90m", "7d"} {
		_, err := ParseFutureTimeArg("start", in, now)
		if !clierr.IsUsage(err) || !strings.Contains(err.Error(), "+"+in) {
			t.Errorf("%q: want a usage error naming +%s, got %v", in, in, err)
		}
	}
}

func TestParseDurationArg(t *testing.T) {
	for in, want := range map[string]time.Duration{"15m": 15 * time.Minute, "2h": 2 * time.Hour, "7d": 7 * 24 * time.Hour, " 1h ": time.Hour} {
		if got, err := ParseDurationArg("duration", in); err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0h", "-1h", "soon", "0d", "1.5d"} {
		if _, err := ParseDurationArg("duration", in); !clierr.IsUsage(err) {
			t.Errorf("%q: want a usage error, got %v", in, err)
		}
	}
}

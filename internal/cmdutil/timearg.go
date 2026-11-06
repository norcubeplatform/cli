package cmdutil

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// ParseTimeArg reads a point in time from a flag such as --since: an RFC
// 3339 timestamp, a date (YYYY-MM-DD, midnight local time), or an age
// before now ("90m", "24h", "7d"). The result is UTC.
func ParseTimeArg(flag, v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, v, time.Local); err == nil {
		return t.UTC(), nil
	}
	if days, ok := strings.CutSuffix(v, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.AddDate(0, 0, -n).UTC(), nil
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(-d).UTC(), nil
	}
	return time.Time{}, clierr.Usage(fmt.Errorf("--%s: %q is not a time; use an RFC 3339 timestamp (2026-10-28T09:00:00Z), a date (2026-10-28) or an age (24h, 7d)", flag, v))
}

// ParseFutureTimeArg reads a point in time from a flag that schedules
// something: an RFC 3339 timestamp, "now", or a span from now ("+2h", "+30m",
// "+7d"). The result is UTC. It leaves judging a timestamp to the API, which
// knows how far back or ahead the thing may be.
//
// A bare "2h" is refused: ParseTimeArg reads that as two hours ago, so taking
// it to mean two hours ahead here would make one spelling mean both.
func ParseFutureTimeArg(flag, v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "now" {
		return now.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if span, ok := strings.CutPrefix(v, "+"); ok {
		if d, ok := parseSpan(span); ok {
			return now.Add(d).UTC(), nil
		}
	}
	if _, ok := parseSpan(v); ok {
		return time.Time{}, clierr.Usage(fmt.Errorf("--%s: %q has no direction; write +%s for that long from now", flag, v, v))
	}
	return time.Time{}, clierr.Usage(fmt.Errorf("--%s: %q is not a time; use an RFC 3339 timestamp (2026-10-28T09:00:00Z), now, or a span from now (+2h, +7d)", flag, v))
}

// ParseDurationArg reads a length from a flag such as --duration: "15m",
// "2h", "7d". It must be more than zero.
func ParseDurationArg(flag, v string) (time.Duration, error) {
	d, ok := parseSpan(strings.TrimSpace(v))
	if !ok {
		return 0, clierr.Usage(fmt.Errorf("--%s: %q is not a length; use for example 15m, 2h or 7d", flag, v))
	}
	return d, nil
}

// parseSpan reads a length of time above zero, in whole days ("7d") or as a
// Go duration ("90m", "1h30m").
func parseSpan(v string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, false
		}
		return time.Duration(n) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

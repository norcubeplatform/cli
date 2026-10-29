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

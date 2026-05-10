package snapdb

import (
	"fmt"
	"time"
)

// formatTimestamp renders a nullable RFC3339 string as a compact "YYYY-MM-DD
// HH:MM:SS" in UTC. Sub-second precision and the trailing "Z" are dropped —
// they're rarely useful in a terminal table and they push rows past the
// terminal width. Returns "—" for nil/empty/unparseable values.
func formatTimestamp(s *string) string {
	if s == nil || *s == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		// Fall back to the first 19 chars so we still produce stable-width
		// output for shapes we don't recognise.
		if len(*s) > 19 {
			return (*s)[:19]
		}
		return *s
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// formatBytes renders a byte count in IEC units. Stays under 8 chars for
// any plausible value ("1023 B", "4.0 GiB", "987 PiB").
func formatBytes(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := int64(n) / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// formatDurationMs renders a duration in milliseconds as a human-readable
// string ("450ms", "12.3s", "2m05s"). Returns "—" for nil. Designed to stay
// under 7 chars.
func formatDurationMs(ms *int) string {
	if ms == nil {
		return "—"
	}
	d := time.Duration(*ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", *ms)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%02dm", h, m)
	}
}

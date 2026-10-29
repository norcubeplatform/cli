package output

import (
	"testing"
	"time"
)

// Time cells render in local time in tables and as RFC 3339 UTC in text.
func TestWhenCells(t *testing.T) {
	prev := time.Local
	time.Local = time.FixedZone("UTC-5", -5*60*60)
	t.Cleanup(func() { time.Local = prev })

	at := time.Date(2026, 10, 28, 17, 55, 50, 77_000_000, time.UTC)
	tests := []struct {
		name, cell, table, text string
	}{
		{"minutes", When(at), "2026-10-28 12:55", "2026-10-28T17:55:50Z"},
		{"seconds", WhenSeconds(at), "2026-10-28 12:55:50", "2026-10-28T17:55:50Z"},
		{"string", WhenString("2026-10-28T19:55:50+02:00"), "2026-10-28 12:55", "2026-10-28T17:55:50Z"},
		{"zero time", When(time.Time{}), placeholder, ""},
		{"empty string", WhenString(""), placeholder, ""},
		{"unparseable string", WhenString("yesterday"), "yesterday", "yesterday"},
	}
	for _, tt := range tests {
		if got := tableCell(tt.cell); got != tt.table {
			t.Errorf("%s: table %q, want %q", tt.name, got, tt.table)
		}
		if got := textCell(tt.cell); got != tt.text {
			t.Errorf("%s: text %q, want %q", tt.name, got, tt.text)
		}
	}
}

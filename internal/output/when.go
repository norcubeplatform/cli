package output

import (
	"strings"
	"time"
)

// Table cells that hold a point in time are written with When or
// WhenSeconds. A table shows them in the reader's local time zone; text
// output (-o text) prints RFC 3339 in UTC, so scripts get one stable,
// parseable form. JSON and YAML encode the underlying values and never see
// these cells.
//
// The cell carries the instant behind a prefix that can't appear in API
// data; the renderers decode it.
const (
	whenPrefix        = "\x00when:"
	whenMinutes       = "m:"
	whenSeconds       = "s:"
	tableMinuteLayout = "2006-01-02 15:04"
	tableSecondLayout = "2006-01-02 15:04:05"
)

// When is a table cell for t, to the minute ("2026-10-28 18:55" in local
// time). A zero time is the "no value" placeholder.
func When(t time.Time) string { return whenCell(t, whenMinutes) }

// WhenSeconds is When to the second.
func WhenSeconds(t time.Time) string { return whenCell(t, whenSeconds) }

// WhenString is When for an RFC 3339 string from an API. An empty string is
// the placeholder; a value that doesn't parse is shown as it is.
func WhenString(s string) string { return whenStringCell(s, whenMinutes) }

// WhenStringSeconds is WhenString to the second.
func WhenStringSeconds(s string) string { return whenStringCell(s, whenSeconds) }

func whenCell(t time.Time, precision string) string {
	if t.IsZero() {
		return placeholder
	}
	return whenPrefix + precision + t.UTC().Format(time.RFC3339Nano)
}

func whenStringCell(s, precision string) string {
	if s == "" {
		return placeholder
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return whenCell(t, precision)
}

// decodeWhen returns the instant and precision of a When cell.
func decodeWhen(cell string) (time.Time, string, bool) {
	rest, ok := strings.CutPrefix(cell, whenPrefix)
	if !ok || len(rest) < 2 {
		return time.Time{}, "", false
	}
	t, err := time.Parse(time.RFC3339Nano, rest[2:])
	if err != nil {
		return time.Time{}, "", false
	}
	return t, rest[:2], true
}

// tableCell renders a cell for the aligned table: When cells in local time.
func tableCell(cell string) string {
	t, precision, ok := decodeWhen(cell)
	if !ok {
		return cell
	}
	if precision == whenSeconds {
		return t.Local().Format(tableSecondLayout)
	}
	return t.Local().Format(tableMinuteLayout)
}

// textTime renders a When cell for -o text: RFC 3339 in UTC, to the second.
func textTime(cell string) (string, bool) {
	t, _, ok := decodeWhen(cell)
	if !ok {
		return "", false
	}
	return t.UTC().Format(time.RFC3339), true
}

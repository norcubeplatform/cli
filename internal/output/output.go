// Package output renders command results in one of three user-selected
// formats: a human-friendly table, JSON, or YAML.
//
// Every service command should hand its result to Print so the global
// --output flag works uniformly. Table rendering is opt-in per command:
// callers describe the columns they want via Table; for JSON/YAML the
// value is encoded as-is.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Format names. Kept as strings so the cobra flag can use them directly.
const (
	FormatTable = "table"
	FormatJSON  = "json"
	FormatYAML  = "yaml"
)

// Table describes how to render a slice of items as a table.
//
//	output.Print(w, format, output.Table[Foo]{
//	    Headers:   []string{"NAME", "ID"},
//	    Rows:      func(f Foo) []string { return []string{f.Name, f.ID} },
//	    Items:     foos,
//	    MaxWidths: []int{40, 0}, // cap NAME at 40 runes; leave ID full
//	    Style:     output.Style{StatusColumn: 1},
//	})
//
// MaxWidths is parallel to Headers. A zero (or out-of-range) entry means
// the column is uncapped. Cells longer than the cap are truncated to
// (max-1) runes and a "…" is appended.
//
// Style controls header-bold, status-color, and dim-placeholder rendering.
// All styling is no-op'd when output isn't an interactive terminal or
// when NO_COLOR is set.
type Table[T any] struct {
	Headers   []string
	Rows      func(T) []string
	Items     []T
	MaxWidths []int
	Style     Style
}

// Print writes value to w in the requested format. For table format, value
// must be a Table[T]; for JSON/YAML, Table[T] is automatically unwrapped to
// its Items slice (the Rows function isn't marshallable).
func Print(w io.Writer, format string, value any) error {
	switch format {
	case FormatJSON, "":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(payload(value))
	case FormatYAML:
		return yaml.NewEncoder(w).Encode(payload(value))
	case FormatTable:
		return printTable(w, value)
	default:
		return fmt.Errorf("unknown output format %q (want table | json | yaml)", format)
	}
}

// payload returns the value that should be encoded for JSON / YAML.
// Table[T] is unwrapped to its Items slice (the Rows function isn't
// marshallable, and a script wants the raw items anyway). Anything else
// passes through unchanged.
func payload(value any) any {
	if e, ok := value.(itemsExposer); ok {
		return e.exposeItems()
	}
	return value
}

type itemsExposer interface {
	exposeItems() any
}

func (t Table[T]) exposeItems() any { return t.Items }

func printTable(w io.Writer, value any) error {
	switch v := value.(type) {
	case interface {
		writeTable(io.Writer) error
	}:
		return v.writeTable(w)
	default:
		return Print(w, FormatJSON, value)
	}
}

func (t Table[T]) writeTable(w io.Writer) error {
	// Build the matrix of *plain* cells first. We truncate here, before
	// styling, so MaxWidths constrains the visible width even when ANSI
	// escapes are added later.
	plain := make([][]string, 0, len(t.Items)+1)
	if len(t.Headers) > 0 {
		plain = append(plain, t.Headers)
	}
	for _, item := range t.Items {
		cells := t.Rows(item)
		for i, c := range cells {
			cells[i] = truncate(c, t.maxWidthAt(i))
		}
		plain = append(plain, cells)
	}
	if len(plain) == 0 {
		return nil
	}

	// Column widths are computed from the plain text so ANSI escape
	// sequences (added below) don't get counted as visible cells.
	nCols := 0
	for _, r := range plain {
		if len(r) > nCols {
			nCols = len(r)
		}
	}
	widths := make([]int, nCols)
	for _, r := range plain {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > widths[i] {
				widths[i] = n
			}
		}
	}

	s := styleFor(w)
	hasHeader := len(t.Headers) > 0
	const colSep = "  "

	for ri, r := range plain {
		isHeader := hasHeader && ri == 0
		var line strings.Builder
		for i, c := range r {
			pad := widths[i] - utf8.RuneCountInString(c)
			styled := c
			switch {
			case isHeader:
				styled = s.styleHeader(c)
			case t.Style.StatusColumn >= 0 && i == t.Style.StatusColumn:
				styled = s.styleStatus(c)
			default:
				styled = s.styleDim(c)
			}
			line.WriteString(styled)
			if i < len(r)-1 {
				// Pad in plain space *after* styling — the ANSI codes have
				// zero visible width, so this aligns correctly.
				line.WriteString(strings.Repeat(" ", pad))
				line.WriteString(colSep)
			}
		}
		line.WriteString("\n")
		if _, err := io.WriteString(w, line.String()); err != nil {
			return err
		}
	}
	return nil
}

func (t Table[T]) maxWidthAt(i int) int {
	if i < len(t.MaxWidths) {
		return t.MaxWidths[i]
	}
	return 0
}

// truncate cuts s to at most max runes, replacing the last rune with "…"
// when truncation occurs. Counts runes, not bytes, so multibyte characters
// (Czech diacritics, em-dashes, etc.) don't get sliced mid-codepoint.
func truncate(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	out := make([]rune, 0, max)
	for _, r := range s {
		if len(out) == max-1 {
			break
		}
		out = append(out, r)
	}
	return string(out) + "…"
}

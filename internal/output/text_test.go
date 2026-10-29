package output

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type job struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes"`
	Note   string `json:"note,omitempty"`
}

var jobs = Table[job]{
	Headers: []string{"ID", "STATUS", "NOTE"},
	Rows: func(j job) []string {
		note := j.Note
		if note == "" {
			note = "—"
		}
		return []string{j.ID, j.Status, note}
	},
	Items: []job{{"j1", "success", 10, ""}, {"j2", "failed", 0, "disk\tfull"}},
	Style: NoStyle,
}

func render(t *testing.T, format string, v any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Print(&buf, format, v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestTextFormat(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"table rows without headers, placeholders empty, tabs flattened", jobs, "j1\tsuccess\t\nj2\tfailed\tdisk full\n"},
		{"object on one line", Object{Value: job{ID: "j1"}, Fields: []Field{{"ID", "j1"}, {"Status", "-"}}}, "j1\t\n"},
		{"scalar", Generic{V: "abc"}, "abc\n"},
		{"number without exponent", Generic{V: float64(1234567890)}, "1234567890\n"},
		{"list of scalars on one line", Generic{V: []any{"a", "b"}}, "a\tb\n"},
		{"list of objects, values in key order", Generic{V: []any{map[string]any{"b": "2", "a": "1"}}}, "1\t2\n"},
		{"null prints nothing", Generic{V: nil}, ""},
		{"plain struct", job{ID: "j9", Status: "ok"}, "0\tj9\tok\n"},
	}
	for _, tt := range tests {
		if got := render(t, FormatText, tt.v); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestObjectFormats(t *testing.T) {
	o := Object{Value: job{ID: "j1", Status: "success"}, Fields: []Field{{"ID", "j1"}, {"Status", "success"}}}
	if got := render(t, FormatJSON, o); !strings.Contains(got, `"status": "success"`) || strings.Contains(got, "Fields") {
		t.Errorf("json is the value itself: %s", got)
	}
	if got, want := render(t, FormatTable, o), "ID:      j1\nStatus:  success\n"; got != want {
		t.Errorf("table: got %q, want %q", got, want)
	}
}

func TestQuery(t *testing.T) {
	g, err := Query(jobs, "[?status=='failed'].id")
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, FormatJSON, g); got != "[\n  \"j2\"\n]\n" {
		t.Errorf("json: %q", got)
	}
	if got := render(t, FormatText, g); got != "j2\n" {
		t.Errorf("text: %q", got)
	}

	g, err = Query(jobs, "[].{id: id, bytes: bytes}")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := render(t, FormatTable, g), "BYTES  ID\n10     j1\n0      j2\n"; got != want {
		t.Errorf("table of a projection: got %q, want %q", got, want)
	}

	var qe *QueryError
	if _, err := Query(jobs, "[?status=="); !errors.As(err, &qe) {
		t.Errorf("invalid expression: %v", err)
	}
	if err := CompileQuery("length(@)"); err != nil {
		t.Errorf("valid expression rejected: %v", err)
	}
}

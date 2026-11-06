package output

import (
	"bytes"
	"errors"
	"testing"
)

func TestPrintID(t *testing.T) {
	type row struct {
		ID string `json:"id"`
	}
	for _, tt := range []struct {
		name string
		v    any
		want string
	}{
		{"object", Object{Value: row{"a"}}, "a\n"},
		{"done", Done{Message: "Made it.", Value: row{"a"}}, "a\n"},
		{"list", Table[row]{Items: []row{{"a"}, {"b"}}}, "a\nb\n"},
		{"empty list", Table[row]{Items: []row{}}, ""},
		{"nothing", Done{Message: "Done."}, ""},
		{"after --query", Generic{V: "a"}, "a\n"},
		{"a number", Generic{V: map[string]any{"id": float64(9002)}}, "9002\n"},
	} {
		var b bytes.Buffer
		if err := Print(&b, FormatID, tt.v); err != nil || b.String() != tt.want {
			t.Errorf("%s: %q, %v; want %q", tt.name, b.String(), err, tt.want)
		}
	}
	var b bytes.Buffer
	if err := Print(&b, FormatID, Object{Value: map[string]any{"name": "x"}}); !errors.Is(err, ErrNoID) {
		t.Errorf("no id: %v", err)
	}
}

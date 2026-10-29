package langsync

import "testing"

func TestLanguageCode(t *testing.T) {
	id := func(n int) *int { return &n }
	codes := map[int]string{4: "ak"}
	tests := []struct {
		name  string
		id    *int
		codes map[int]string
		want  string
	}{
		{"known id shows its code", id(4), codes, "ak"},
		{"unknown id falls back to the id", id(7), codes, "7"},
		{"failed lookup falls back to the id", id(4), nil, "4"},
		{"no default language", nil, codes, intStr(nil)},
	}
	for _, tt := range tests {
		if got := languageCode(tt.id, tt.codes); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

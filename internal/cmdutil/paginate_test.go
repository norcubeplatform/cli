package cmdutil

import (
	"context"
	"fmt"
	"testing"
)

// pages serves n items in pages, cursors being the next item's index.
func pages(n int, calls *int) FetchFunc[int] {
	return func(_ context.Context, cursor string, size int) ([]int, string, error) {
		*calls++
		start := 0
		if cursor != "" {
			_, _ = fmt.Sscanf(cursor, "%d", &start)
		}
		var out []int
		for i := start; i < n && len(out) < size; i++ {
			out = append(out, i)
		}
		next := ""
		if start+len(out) < n {
			next = fmt.Sprint(start + len(out))
		}
		return out, next, nil
	}
}

func TestPaginate(t *testing.T) {
	tests := []struct {
		name               string
		pf                 PageFlags
		n                  int
		wantItems, wantReq int
		wantNext           string
		wantTrunc          bool
	}{
		{"one page by default", PageFlags{}, 25, 10, 1, "10", false},
		{"page size", PageFlags{PageSize: 4}, 25, 4, 1, "4", false},
		{"cursor continues", PageFlags{Cursor: "20"}, 25, 5, 1, "", false},
		{"all pages", PageFlags{AllPages: true}, 25, 25, 3, "", false},
		{"all pages capped", PageFlags{AllPages: true, MaxItems: 15}, 25, 20, 2, "20", true},
		{"empty", PageFlags{}, 0, 0, 1, "", false},
	}
	for _, tt := range tests {
		tt.pf.defaultPageSize, tt.pf.defaultMaxItems = 10, 100
		calls := 0
		p, err := Paginate(context.Background(), tt.pf, pages(tt.n, &calls))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if p.Items == nil {
			t.Errorf("%s: nil items would print as null", tt.name)
		}
		if len(p.Items) != tt.wantItems || calls != tt.wantReq || p.NextCursor != tt.wantNext || p.Truncated != tt.wantTrunc {
			t.Errorf("%s: %d items, %d requests, next %q, truncated %v; want %d, %d, %q, %v",
				tt.name, len(p.Items), calls, p.NextCursor, p.Truncated, tt.wantItems, tt.wantReq, tt.wantNext, tt.wantTrunc)
		}
	}
	if _, err := Paginate(context.Background(), PageFlags{PageSize: -1}, pages(1, new(int))); err == nil {
		t.Error("negative page size accepted")
	}
}

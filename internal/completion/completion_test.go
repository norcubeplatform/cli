package completion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestFilterMatchesValuesNotDescriptions(t *testing.T) {
	got := filter([]cobra.Completion{"acme\tAcme Inc", "home\tacme's home", "beta"}, "ac")
	if len(got) != 1 || got[0] != "acme\tAcme Inc" {
		t.Errorf("got %v", got)
	}
}

func TestCacheExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeCache(path, []cobra.Completion{"a"})
	if items, ok := readCache(path); !ok || len(items) != 1 {
		t.Fatalf("fresh cache not read: %v %v", items, ok)
	}
	stale, _ := json.Marshal(cacheFile{At: time.Now().Add(-TTL - time.Second), Items: []cobra.Completion{"a"}})
	if err := os.WriteFile(path, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCache(path); ok {
		t.Error("stale cache reused")
	}
	future, _ := json.Marshal(cacheFile{At: time.Now().Add(time.Hour), Items: []cobra.Completion{"a"}})
	_ = os.WriteFile(path, future, 0o600)
	if _, ok := readCache(path); ok {
		t.Error("cache from the future reused (clock change)")
	}
}

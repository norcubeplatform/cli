package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// complete runs the shell's completion request and returns the offered
// values (descriptions dropped) and the directive line.
func complete(t *testing.T, args ...string) ([]string, string) {
	t.Helper()
	stdout, _, err := run(t, append([]string{"__complete"}, args...)...)
	if err != nil {
		t.Fatalf("__complete %v: %v", args, err)
	}
	var values []string
	directive := ""
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, ":") {
			directive = line
			continue
		}
		v, _, _ := strings.Cut(line, "\t")
		if v != "" {
			values = append(values, v)
		}
	}
	return values, directive
}

func clearCompletionCache(t *testing.T) {
	t.Helper()
	dir, _ := os.UserCacheDir()
	_ = os.RemoveAll(filepath.Join(dir, "norcube", "completion"))
}

func TestCompleteOrganizationSlugs(t *testing.T) {
	signIn(t)
	clearCompletionCache(t)
	got, directive := complete(t, "org", "use", "")
	if strings.Join(got, ",") != "home,acme" {
		t.Errorf("org use: %v", got)
	}
	if directive != ":4" { // ShellCompDirectiveNoFileComp
		t.Errorf("directive %q", directive)
	}
	backend.mu.Lock()
	lists := backend.lists
	backend.mu.Unlock()

	// A second request within the minute is served from the cache, and
	// the prefix filters it.
	got, _ = complete(t, "backup", "jobs", "list", "--org", "ac")
	if strings.Join(got, ",") != "acme" {
		t.Errorf("--org ac: %v", got)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.lists != lists {
		t.Errorf("the cached answer wasn't reused: %d list calls, want %d", backend.lists, lists)
	}
}

// A lookup that fails offers nothing and never errors or prompts.
func TestCompletionFailsSilently(t *testing.T) {
	signIn(t)
	clearCompletionCache(t)
	backend.mu.Lock()
	backend.refresh = "" // session revoked
	backend.mu.Unlock()
	got, directive := complete(t, "org", "use", "")
	if len(got) != 0 || directive != ":4" {
		t.Errorf("got %v %q", got, directive)
	}
	// The fake backend has no data source API: nothing, quietly.
	signIn(t)
	got, _ = complete(t, "backup", "datasources", "get", "")
	if len(got) != 0 {
		t.Errorf("datasources get: %v", got)
	}
}

func TestCompleteProfiles(t *testing.T) {
	got, _ := complete(t, "whoami", "--profile", "")
	if len(got) == 0 || got[0] != "default" {
		t.Errorf("profiles: %v", got)
	}
}

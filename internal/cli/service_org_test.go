package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// LangSync commands act in the organization --org names, else the one the
// project's .langsync.json pins, else the profile's. The token the CLI
// mints shows which one it picked.
func TestLangsyncOrganizationPrecedence(t *testing.T) {
	t.Setenv("NORCUBE_LANGSYNC_URL", backend.srv.URL)
	project := t.TempDir()
	pinned := `{"version":1,"organization":{"id":"` + acmeOrgID + `","slug":"acme"},"namespaces":[{"namespace":"web","dir":"locales","format":"flat-json","default_local_language":"en"}]}`
	if err := os.WriteFile(filepath.Join(project, ".langsync.json"), []byte(pinned), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()

	for _, tc := range []struct {
		name, dir string
		args      []string
		wantOrg   string
	}{
		{"profile outside a project", elsewhere, nil, homeOrgID},
		{"project file inside a project", project, nil, acmeOrgID},
		{"--org beats the project file", project, []string{"--org", "home"}, homeOrgID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signIn(t)
			t.Chdir(tc.dir)
			// The fake backend has no LangSync API; the request fails after
			// the token is minted, which is all this test needs.
			_, _, _ = run(t, append([]string{"langsync", "namespace", "list"}, tc.args...)...)
			if want := "langsync-api/" + tc.wantOrg; !slices.Contains(backend.mintedTokens(), want) {
				t.Errorf("minted %v, want a token for %s", backend.mintedTokens(), want)
			}
		})
	}
}

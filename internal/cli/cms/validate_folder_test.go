package cms

import (
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// A repository whose site lives in a folder (a monorepo) is not a site at
// its root, and the refusal names the folder to pass.
func TestValidateNamesTheSiteFolder(t *testing.T) {
	repo := t.TempDir()
	if err := os.CopyFS(repo+"/website", os.DirFS(qualitySite)); err != nil {
		t.Fatal(err)
	}
	_, _, err := execValidate(t, validateDeps{}, repo)
	var cerr *clierr.Error
	if !errors.As(err, &cerr) || cerr.Code != clierr.CodeNotFound {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(cerr.Hint, "website/") || !strings.Contains(cerr.Hint, "nrc cms validate "+repo+"/website") {
		t.Fatalf("hint = %q", cerr.Hint)
	}
}

func TestSiteFoldersBelow(t *testing.T) {
	fsys := fstest.MapFS{
		"website/.norcube/cms/config.yml":        {},
		"apps/docs/norcube-cms.config.yml":       {},
		"a/b/c/.norcube/cms/config.yml":          {}, // too deep
		"node_modules/x/.norcube/cms/config.yml": {},
		".hidden/.norcube/cms/config.yml":        {},
		"backend/main.go":                        {},
	}
	got := strings.Join(siteFoldersBelow(fsys, siteFolderDepth), ",")
	if got != "apps/docs,website" {
		t.Fatalf("siteFoldersBelow = %q", got)
	}
	if h := notASiteHint(".", nil); !strings.Contains(h, "site's root") {
		t.Fatalf("hint without a folder = %q", h)
	}
}

package cms

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/norcubeplatform/cms-schema/gitlog"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// gitPages is the pages collection of qualitySite with a link to judge:
// a changed link is nothing to translate.
const gitPages = `label: Pages
path: src/content/pages
match: '**/*.md'
previewPath: '/{locale}/{slug}/'
localeFrom: path
translations:
  by: path
fields:
  - {key: title, type: text, label: Title, required: true, role: seoTitle}
  - {key: slug, type: text, label: Slug}
  - {key: more, type: link, label: Read more}
`

func gitPage(title, more string) string {
	return "---\ntitle: " + title + "\nslug: about\nmore: " + more + "\n---\n"
}

// gitRepo is a repository holding qualitySite in website/, as a monorepo
// holds its site.
type gitRepo struct {
	t    *testing.T
	root string
	secs int64
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	// git must not find a repository above the temporary directory, nor
	// read the machine's configuration (signing, hooks).
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := os.CopyFS(filepath.Join(root, "website"), os.DirFS(qualitySite)); err != nil {
		t.Fatal(err)
	}
	return &gitRepo{t: t, root: root, secs: 1_000_000_000}
}

func (r *gitRepo) site() string { return filepath.Join(r.root, "website") }

func (r *gitRepo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root}, args...)...) // #nosec G204 -- fixed git arguments of this test
	date := "@" + strconv.FormatInt(r.secs, 10) + " +0000"
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// write writes files of the site, paths relative to website/.
func (r *gitRepo) write(files map[string]string) {
	r.t.Helper()
	for p, body := range files {
		if err := os.WriteFile(filepath.Join(r.site(), filepath.FromSlash(p)), []byte(body), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
}

// commit writes and commits files months after the previous commit: the
// dates must never matter, only what changed.
func (r *gitRepo) commit(files map[string]string) {
	r.t.Helper()
	r.write(files)
	r.secs += 200 * 24 * 3600
	r.git("add", ".")
	r.git("commit", "-q", "-m", "change")
}

// outdated runs `validate --quality --git` on the site and returns the
// translation_outdated rows.
func (r *gitRepo) outdated() []string {
	r.t.Helper()
	out, _, err := execValidate(r.t, validateDeps{sourceBases: gitlog.SourceBases}, r.site(), "--quality", "--git", "--fail-on", "none")
	if err != nil {
		r.t.Fatal(err)
	}
	return outdatedLines(out)
}

func TestValidateGitJudgesChangedTexts(t *testing.T) {
	r := newGitRepo(t)
	r.git("init", "-q")
	r.commit(map[string]string{
		".norcube/cms/collections/pages.yml": gitPages,
		qualityEnPage:                        gitPage("About the studio", "/cs/about/"),
		qualityCsPage:                        gitPage("O ateliéru", "/en/about/"),
	})
	if got := r.outdated(); len(got) != 0 {
		t.Fatalf("both languages in one commit: %v", got)
	}

	// The source's link changes months later: nothing to translate.
	r.commit(map[string]string{qualityEnPage: gitPage("About the studio", "/cs/about/#team")})
	if got := r.outdated(); len(got) != 0 {
		t.Fatalf("a link change made the translation outdated: %v", got)
	}

	// The source's title changes: the translation is behind.
	r.commit(map[string]string{qualityEnPage: gitPage("About the studio and its people", "/cs/about/#team")})
	got := r.outdated()
	if len(got) != 1 || !strings.Contains(got[0], qualityCsPage) || !strings.Contains(got[0], "title") {
		t.Fatalf("a title change: %v", got)
	}

	// The translation is being updated in the working tree: it is current.
	r.write(map[string]string{qualityCsPage: gitPage("O ateliéru a lidech", "/en/about/")})
	if got := r.outdated(); len(got) != 0 {
		t.Fatalf("a translation changed in the working tree: %v", got)
	}
}

func TestValidateGitOutsideACheckout(t *testing.T) {
	r := newGitRepo(t)
	_, _, err := execValidate(t, validateDeps{sourceBases: gitlog.SourceBases}, r.site(), "--quality", "--git")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != clierr.CodeUsage || !errors.Is(err, gitlog.ErrNotARepository) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(ce.Hint, "clone") {
		t.Errorf("hint %q", ce.Hint)
	}
}

// A shallow clone (`--depth 1`, the default of CI checkouts) would make
// every translation look current: --git refuses it with a usage error
// that says how to fetch the full history.
func TestValidateGitShallowClone(t *testing.T) {
	r := newGitRepo(t)
	r.git("init", "-q")
	r.commit(map[string]string{
		".norcube/cms/collections/pages.yml": gitPages,
		qualityEnPage:                        gitPage("About the studio", "/cs/about/"),
		qualityCsPage:                        gitPage("O ateliéru", "/en/about/"),
	})
	r.commit(map[string]string{qualityEnPage: gitPage("About the studio and its people", "/cs/about/")})
	if got := r.outdated(); len(got) != 1 {
		t.Fatalf("the full repository: %v", got)
	}

	shallow := filepath.Join(t.TempDir(), "shallow")
	r.git("clone", "-q", "--depth", "1", "file://"+r.root, shallow)
	site := filepath.Join(shallow, "website")
	out, _, err := execValidate(t, validateDeps{sourceBases: gitlog.SourceBases}, site, "--quality", "--git")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != clierr.CodeUsage || !errors.Is(err, gitlog.ErrShallowHistory) {
		t.Fatalf("got %v", err)
	}
	if want := "--git: " + site + " is a shallow clone"; !strings.HasPrefix(ce.Message, want) {
		t.Errorf("message %q", ce.Message)
	}
	if !strings.Contains(ce.Hint, "fetch-depth: 0") || !strings.Contains(ce.Hint, "git fetch --unshallow") {
		t.Errorf("hint %q", ce.Hint)
	}
	if out != "" {
		t.Errorf("a report was printed: %q", out)
	}
}

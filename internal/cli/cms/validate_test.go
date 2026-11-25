package cms

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/norcubeplatform/cms-schema/gitlog"
	"github.com/norcubeplatform/cms-schema/quality"
	"github.com/norcubeplatform/cms-schema/report"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// qualitySite holds pages in two languages linked by path and works that
// hold both languages in one file.
const qualitySite = "../testdata/cms-site-quality"

// execValidate runs `validate` built with deps, as the root would.
func execValidate(t *testing.T, deps validateDeps, args ...string) (string, string, error) {
	t.Helper()
	cmd := newValidateCmd(deps)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

// The pages of qualitySite: English is the default language, so the
// English page is the source of the Czech one.
const (
	qualityEnPage = "src/content/pages/en/about.md"
	qualityCsPage = "src/content/pages/cs/about.md"
)

// fakeSourceBases answers every query with base, and counts the readers
// opened and the calls made.
type fakeSourceBases struct {
	t       *testing.T
	base    func(q quality.BaseQuery) quality.SourceBase
	err     error
	opened  int
	read    int
	queries []quality.BaseQuery
}

func (f *fakeSourceBases) open(ctx context.Context, dir string, _ gitlog.Options) (quality.BaseReader, func()) {
	f.opened++
	if dir != qualitySite {
		f.t.Errorf("git read %q, want the site's path", dir)
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > gitlog.DefaultTimeout {
		f.t.Errorf("no deadline of at most %s: %v %v", gitlog.DefaultTimeout, deadline, ok)
	}
	return func(q []quality.BaseQuery) ([]quality.SourceBase, error) {
		f.read++
		f.queries = append(f.queries, q...)
		if f.err != nil {
			return nil, f.err
		}
		out := make([]quality.SourceBase, len(q))
		for i := range q {
			out[i] = f.base(q[i])
		}
		return out, nil
	}, func() {}
}

// outdatedLines are the table rows of quality.translation_outdated.
func outdatedLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, string(report.RuleQualityTranslationOutdated)) {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestValidateGitHistoryReachesQuality(t *testing.T) {
	fake := &fakeSourceBases{t: t, base: func(q quality.BaseQuery) quality.SourceBase {
		if q.Translation != qualityCsPage || q.Source != qualityEnPage {
			return quality.SourceBase{}
		}
		// The English title was different when the Czech page last changed.
		return quality.SourceBase{Known: true, Content: []byte("---\ntitle: About us\nslug: about\n---\n")}
	}}
	deps := validateDeps{sourceBases: fake.open}

	out, stderr, err := execValidate(t, deps, qualitySite, "--quality")
	if err != nil {
		t.Fatal(err)
	}
	if fake.opened != 0 {
		t.Fatalf("git ran without --git")
	}
	if got := outdatedLines(out); len(got) != 0 {
		t.Fatalf("outdated translations reported without the history:\n%s", out)
	}
	if !strings.Contains(stderr, "skipped quality.translation_outdated") || !strings.Contains(stderr, "--git") {
		t.Errorf("the skipped rule is not explained: %q", stderr)
	}

	out, stderr, err = execValidate(t, deps, qualitySite, "--quality", "--git")
	if err != nil {
		t.Fatal(err)
	}
	if fake.opened != 1 || fake.read != 1 {
		t.Fatalf("history opened %d times, read %d times", fake.opened, fake.read)
	}
	got := outdatedLines(out)
	if len(got) != 1 || !strings.Contains(got[0], qualityCsPage) || !strings.Contains(got[0], "title") {
		t.Fatalf("the Czech page whose source changed its title is not reported once:\n%s", out)
	}
	if strings.Contains(stderr, "skipped quality.translation_outdated") {
		t.Errorf("the rule is still skipped with --git: %q", stderr)
	}

	// A base that says the source is the same: nothing to translate.
	fake.base = func(quality.BaseQuery) quality.SourceBase { return quality.SourceBase{Known: true, Same: true} }
	out, _, err = execValidate(t, deps, qualitySite, "--quality", "--git")
	if err != nil {
		t.Fatal(err)
	}
	if got := outdatedLines(out); len(got) != 0 {
		t.Fatalf("an unchanged source made a translation outdated:\n%s", out)
	}
}

// A history that can't be read ends the run with an error that says why:
// --git asked for it, so the check is not silently skipped.
func TestValidateGitHistoryFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"not a checkout is a usage error", gitlog.ErrNotARepository, clierr.CodeUsage},
		{"a shallow clone is a usage error", fmt.Errorf("%w: site", gitlog.ErrShallowHistory), clierr.CodeUsage},
		{"too slow is a timeout", fmt.Errorf("git log: %w", context.DeadlineExceeded), clierr.CodeTimeout},
		{"anything else is an error", errors.New("git log: exit status 128"), clierr.CodeError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeSourceBases{t: t, err: c.err}
			out, _, err := execValidate(t, validateDeps{sourceBases: fake.open}, qualitySite, "--quality", "--git")
			var ce *clierr.Error
			if !errors.As(err, &ce) || ce.Code != c.code || !errors.Is(err, c.err) {
				t.Fatalf("got %v", err)
			}
			if !strings.Contains(ce.Message, "--git") || ce.Hint == "" {
				t.Errorf("message %q hint %q", ce.Message, ce.Hint)
			}
			if out != "" {
				t.Errorf("a report was printed: %q", out)
			}
		})
	}

	t.Run("an interrupt stays an interrupt", func(t *testing.T) {
		fake := &fakeSourceBases{t: t, err: fmt.Errorf("git log: %w", context.Canceled)}
		_, _, err := execValidate(t, validateDeps{sourceBases: fake.open}, qualitySite, "--quality", "--git")
		var ce *clierr.Error
		if !errors.Is(err, context.Canceled) || errors.As(err, &ce) {
			t.Fatalf("got %v", err)
		}
	})

	// git killed by the deadline fails in any way (a killed
	// `git rev-parse` reads as "not a repository"): the context decides.
	t.Run("a passed deadline is a timeout whatever git said", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		var ce *clierr.Error
		if err := gitHistoryError(ctx, "site", gitlog.ErrNotARepository); !errors.As(err, &ce) || ce.Code != clierr.CodeTimeout {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an interrupt is an interrupt whatever git said", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var ce *clierr.Error
		if err := gitHistoryError(ctx, "site", errors.New("signal: killed")); !errors.Is(err, context.Canceled) || errors.As(err, &ce) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestFindingPathShowsTheLanguage(t *testing.T) {
	cases := []struct {
		f    report.Finding
		want string
	}{
		{report.Finding{Path: "title"}, "title"},
		{report.Finding{Path: "title.cs", Locale: "cs"}, "title.cs [cs]"},
		{report.Finding{Locale: "en"}, "[en]"},
		{report.Finding{}, ""},
	}
	for _, c := range cases {
		if got := findingPath(c.f); got != c.want {
			t.Errorf("findingPath(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

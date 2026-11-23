package cms

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/norcubeplatform/cms-schema/gitlog"
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

func TestValidateGitDatesReachQuality(t *testing.T) {
	now := time.Now()
	calls := 0
	deps := validateDeps{lastChanged: func(_ context.Context, dir string, _ gitlog.Options) (map[string]time.Time, error) {
		calls++
		if dir != qualitySite {
			t.Errorf("git read %q, want the site's path", dir)
		}
		return map[string]time.Time{
			"src/content/pages/en/about.md": now,
			"src/content/pages/cs/about.md": now.Add(-30 * 24 * time.Hour),
		}, nil
	}}

	out, stderr, err := execValidate(t, deps, qualitySite, "--quality")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("git ran without --git")
	}
	if strings.Contains(out, string(report.RuleQualityTranslationOutdated)) {
		t.Fatalf("outdated translations reported without dates:\n%s", out)
	}
	if !strings.Contains(stderr, "skipped quality.translation_outdated") || !strings.Contains(stderr, "--git") {
		t.Errorf("the skipped rule is not explained: %q", stderr)
	}

	out, stderr, err = execValidate(t, deps, qualitySite, "--quality", "--git")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("git ran %d times", calls)
	}
	var outdated string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, string(report.RuleQualityTranslationOutdated)) {
			outdated = line
		}
	}
	if !strings.Contains(outdated, "src/content/pages/cs/about.md") {
		t.Fatalf("the older language version is not reported as outdated:\n%s", out)
	}
	if strings.Contains(stderr, "skipped quality.translation_outdated") {
		t.Errorf("the rule is still skipped with --git: %q", stderr)
	}
}

func TestReadGitDates(t *testing.T) {
	t.Run("a deadline and the path", func(t *testing.T) {
		want := map[string]time.Time{"a.md": time.Unix(1, 0)}
		got, err := readGitDates(context.Background(), "site", func(ctx context.Context, dir string, _ gitlog.Options) (map[string]time.Time, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > gitlog.DefaultTimeout {
				t.Errorf("no deadline of at most %s: %v %v", gitlog.DefaultTimeout, deadline, ok)
			}
			if dir != "site" {
				t.Errorf("dir %q", dir)
			}
			return want, nil
		})
		if err != nil || len(got) != 1 || !got["a.md"].Equal(want["a.md"]) {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	cases := []struct {
		name string
		err  error
		code string
	}{
		{"not a checkout is a usage error", gitlog.ErrNotARepository, clierr.CodeUsage},
		{"too slow is a timeout", context.DeadlineExceeded, clierr.CodeTimeout},
		{"anything else is an error", errors.New("git log: exit status 128"), clierr.CodeError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dates, err := readGitDates(context.Background(), "site", func(context.Context, string, gitlog.Options) (map[string]time.Time, error) {
				return nil, c.err
			})
			var ce *clierr.Error
			if dates != nil || !errors.As(err, &ce) || ce.Code != c.code || !errors.Is(err, c.err) {
				t.Fatalf("got %v, %v", dates, err)
			}
			if !strings.Contains(ce.Message, "--git") || ce.Hint == "" {
				t.Errorf("message %q hint %q", ce.Message, ce.Hint)
			}
		})
	}

	t.Run("an interrupt stays an interrupt", func(t *testing.T) {
		_, err := readGitDates(context.Background(), "site", func(context.Context, string, gitlog.Options) (map[string]time.Time, error) {
			return nil, context.Canceled
		})
		var ce *clierr.Error
		if !errors.Is(err, context.Canceled) || errors.As(err, &ce) {
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

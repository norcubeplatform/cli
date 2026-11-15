package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

// cms validate needs no session: an empty, isolated config is enough.
func cmsEnv(t *testing.T) {
	t.Helper()
	isolatedConfig(t, func(*config.File) {})
}

func TestCmsValidateExitCodes(t *testing.T) {
	t.Run("errors fail by default", func(t *testing.T) {
		cmsEnv(t)
		out, stderr, err := run(t, "cms", "validate", "testdata/cms-site-broken", "-o", "json")
		if err == nil {
			t.Fatal("expected an error")
		}
		e := Describe(err)
		if e.Code != clierr.CodeInvalid || e.ExitCode() != clierr.ExitError {
			t.Fatalf("code %q exit %d", e.Code, e.ExitCode())
		}
		// The report is still printed, so CI keeps the findings.
		var rep struct {
			Findings []struct {
				File, Path, Rule, Severity string
			}
			Summary struct{ Errors, Warnings int }
		}
		if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
			t.Fatalf("stdout is not the JSON report: %v\n%s", jerr, out)
		}
		hasRequired := false
		for _, f := range rep.Findings {
			if f.Rule == "content.required" && f.Path == "title" {
				hasRequired = true
			}
		}
		if rep.Summary.Errors != 1 || !hasRequired {
			t.Fatalf("report: %+v", rep)
		}
		if !strings.Contains(stderr, "1 error") {
			t.Errorf("summary missing on stderr: %q", stderr)
		}
	})
	t.Run("fail-on none always passes", func(t *testing.T) {
		cmsEnv(t)
		if _, _, err := run(t, "cms", "validate", "testdata/cms-site-broken", "--fail-on", "none", "-o", "json"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("fail-on warning fails on warnings", func(t *testing.T) {
		cmsEnv(t)
		_, _, err := run(t, "cms", "validate", "testdata/cms-site", "--fail-on", "warning", "-o", "json")
		if e := Describe(err); e == nil || e.Code != clierr.CodeInvalid {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("warnings alone pass by default", func(t *testing.T) {
		cmsEnv(t)
		if _, _, err := run(t, "cms", "validate", "testdata/cms-site", "-o", "json"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestCmsValidateRejectsBadInvocations(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code string
		exit int
	}{
		{"not a site", []string{"cms", "validate", "testdata"}, clierr.CodeNotFound, clierr.ExitNotFound},
		{"missing directory", []string{"cms", "validate", "testdata/nope"}, clierr.CodeUsage, clierr.ExitUsage},
		{"a file, not a directory", []string{"cms", "validate", "testdata/cms-site/.norcube/cms/config.yml"}, clierr.CodeUsage, clierr.ExitUsage},
		{"unknown scope", []string{"cms", "validate", "testdata/cms-site", "--only", "everything"}, clierr.CodeUsage, clierr.ExitUsage},
		{"unknown fail-on", []string{"cms", "validate", "testdata/cms-site", "--fail-on", "sometimes"}, clierr.CodeUsage, clierr.ExitUsage},
		{"two paths", []string{"cms", "validate", "a", "b"}, clierr.CodeUsage, clierr.ExitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmsEnv(t)
			_, _, err := run(t, c.args...)
			e := Describe(err)
			if e == nil || e.Code != c.code || e.ExitCode() != c.exit {
				t.Fatalf("got %v (code %v)", err, e)
			}
		})
	}
}

func TestCmsValidateOnlyAndCollection(t *testing.T) {
	cmsEnv(t)
	out, _, err := run(t, "cms", "validate", "testdata/cms-site", "--only", "schema", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Findings []any
		Summary  struct{ Files int }
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Files != 0 || len(rep.Findings) != 0 {
		t.Fatalf("schema-only run touched content: %+v", rep)
	}
	out, _, err = run(t, "cms", "validate", "testdata/cms-site", "--collection", "ghost", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Files != 0 {
		t.Fatalf("an unknown collection should match no file: %+v", rep)
	}
}

func TestCmsValidateIsLocal(t *testing.T) {
	// No endpoint, no token, no network: the command must not care.
	cmsEnv(t)
	t.Setenv("NORCUBE_TOKEN", "")
	if _, _, err := run(t, "cms", "validate", "testdata/cms-site", "--endpoint-url", "auth=http://127.0.0.1:9", "-o", "json"); err != nil {
		t.Fatalf("a local command reached for the network: %v", err)
	}
	var notASite *clierr.Error
	_, _, err := run(t, "cms", "validate", "testdata")
	if !errors.As(err, &notASite) || notASite.Code != clierr.CodeNotFound {
		t.Fatalf("got %v", err)
	}
}

package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

// A profile with no session asks for a login before anything else, even
// when it has no organization either: "sign in" fixes it, "no active
// organization" would send the user the wrong way.
func TestSignedOutProfileAsksForLoginFirst(t *testing.T) {
	b := newBackend(t)
	isolatedConfig(t, func(f *config.File) {
		customProfile(f, "p-out", b, config.Organization{})
		customProfile(f, "p-out-org", b, config.Organization{ID: homeOrgID, Slug: "home"})
	})

	for _, args := range [][]string{
		{"backup", "list", "--profile", "p-out"},
		{"backup", "datasource", "list", "--profile", "p-out"},
		{"langsync", "namespace", "list", "--profile", "p-out"},
		{"org", "list", "--profile", "p-out"},
		{"whoami", "--profile", "p-out"},
		{"backup", "list", "--profile", "p-out", "--org", "acme"},
		{"backup", "list", "--profile", "p-out-org"},
	} {
		_, _, err := run(t, args...)
		if !errors.Is(err, auth.ErrNotLoggedIn) || clierr.IsUsage(err) {
			t.Errorf("%s: got %v, want not signed in", strings.Join(args, " "), err)
		}
	}
	if got := b.mintedTokens(); len(got) != 0 {
		t.Errorf("a signed-out profile called the server: %v", got)
	}
}

// The login-required message is one hint, naming the profile only when
// --profile or NORCUBE_PROFILE chose it.
func TestLoginHint(t *testing.T) {
	b := newBackend(t)
	isolatedConfig(t, func(f *config.File) {
		customProfile(f, config.DefaultProfileName, b, config.Organization{ID: homeOrgID, Slug: "home"})
		customProfile(f, "p-out", b, config.Organization{})
	})

	_, _, err := run(t, "backup", "list", "--profile", "p-out")
	if got, want := LoginHint(err), "Not signed in (profile p-out). Run `norcube login --profile p-out`."; got != want {
		t.Errorf("explicit profile: %q, want %q", got, want)
	}

	_, _, err = run(t, "backup", "list")
	if got, want := LoginHint(err), "Not signed in. Run `norcube login`."; got != want {
		t.Errorf("default profile: %q, want %q", got, want)
	}

	// A session the server refuses: its reason, then the one fix.
	if err := auth.SaveRefreshToken(config.DefaultProfileName, b.srv.URL, "revoked"); err != nil {
		t.Fatal(err)
	}
	_, _, err = run(t, "backup", "list")
	if !errors.Is(err, auth.ErrLoginRequired) || errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("refused session: %v", err)
	}
	got := LoginHint(err)
	if !strings.HasSuffix(got, "Run `norcube login` to sign in again.") || strings.Count(got, "norcube login") != 1 {
		t.Errorf("refused session hint: %q", got)
	}
}

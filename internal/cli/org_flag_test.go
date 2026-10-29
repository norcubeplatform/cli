package cli

import (
	"errors"
	"slices"
	"testing"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/orgs"
)

func TestOrgFlagAcceptsSlug(t *testing.T) {
	signIn(t)
	if _, stderr, err := run(t, "--org", "acme", "backup", "list", "-o", "json"); err != nil {
		t.Fatalf("backup list --org acme: %v\n%s", err, stderr)
	}
	minted := backend.mintedTokens()
	if !slices.Contains(minted, auth.AudienceSnapDB+"/"+acmeOrgID) {
		t.Fatalf("Backup token was not minted for acme's id; minted %v", minted)
	}

	// The slug is cached: a second command doesn't list again.
	before := backend.lists
	if _, _, err := run(t, "--org", "acme", "backup", "list", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if backend.lists != before {
		t.Fatalf("slug looked up again within the cache TTL")
	}
}

func TestOrgFlagAcceptsID(t *testing.T) {
	signIn(t)
	if _, _, err := run(t, "--org", acmeOrgID, "backup", "list", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if backend.lists != 0 {
		t.Fatalf("an id should not need /organizations (lists=%d)", backend.lists)
	}
}

func TestOrgFlagUnknownSlugIsUsageError(t *testing.T) {
	signIn(t)
	_, _, err := run(t, "--org", "nope", "backup", "list")
	var nf *orgs.NotFoundError
	if !errors.As(err, &nf) || !clierr.IsUsage(err) {
		t.Fatalf("want a NotFoundError usage error, got %v", err)
	}
	if errors.Is(err, auth.ErrLoginRequired) {
		t.Fatal("an unknown slug must not read as an expired session")
	}
}

func TestOrgUseBySlug(t *testing.T) {
	signIn(t)
	out, _, err := run(t, "org", "use", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Active organization set to acme (" + acmeOrgID + ").\n"; out != want {
		t.Fatalf("stdout %q, want %q", out, want)
	}
	// Restore the active org for the other tests.
	if _, _, err := run(t, "org", "use", "home"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "org", "use", "nope"); !clierr.IsUsage(err) {
		t.Fatalf("org use with an unknown slug: want usage error, got %v", err)
	}
}

// A refused organization reads as "not a member" (exit 5), never as an
// expired session, and the session keeps working.
func TestForeignOrgIsAccessDeniedNotLoginRequired(t *testing.T) {
	signIn(t)
	_, _, err := run(t, "--org", otherOrgID, "backup", "list")
	if !errors.Is(err, auth.ErrOrgAccessDenied) || errors.Is(err, auth.ErrLoginRequired) {
		t.Fatalf("want ErrOrgAccessDenied, got %v", err)
	}
	if _, _, err := run(t, "backup", "list", "-o", "json"); err != nil {
		t.Fatalf("session broken after the refused org: %v", err)
	}
}

// A dead session is still "login required" when an org is given.
func TestDeadSessionWithOrgIsLoginRequired(t *testing.T) {
	signIn(t)
	backend.mu.Lock()
	backend.refresh = ""
	backend.mu.Unlock()
	_, _, err := run(t, "--org", acmeOrgID, "backup", "list")
	if !errors.Is(err, auth.ErrLoginRequired) {
		t.Fatalf("want ErrLoginRequired, got %v", err)
	}
}

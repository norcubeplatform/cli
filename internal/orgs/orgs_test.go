package orgs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/clierr"
)

const (
	acmeID  = "0b9d2b8e-1c1f-4c3e-9a51-6d1c2f9a7e01"
	otherID = "6f1e8a52-3d4b-4f7a-8c2e-9b0a1d2c3e4f"
	homeID  = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
)

var memberOf = []api.Organization{
	{ID: homeID, Slug: "home", Name: "Home"},
	{ID: acmeID, Slug: "acme", Name: "Acme Inc"},
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantID    string
		wantSlug  string
		wantCalls int
		wantErr   error // matched with errors.As against its type
		wantUsage bool
	}{
		{"slug resolves via the list", "acme", acmeID, "acme", 1, nil, false},
		{"slug match ignores case", "ACME", acmeID, "acme", 1, nil, false},
		{"id passes through without a call", otherID, otherID, "", 0, nil, false},
		{"id of the active org gets its slug", homeID, homeID, "home", 0, nil, false},
		{"active slug needs no call", "home", homeID, "home", 0, nil, false},
		{"unknown slug is a usage error", "nope", "", "", 1, &NotFoundError{}, true},
		{"blank is a usage error", "  ", "", "", 0, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			r := &Resolver{
				AuthURL: "https://auth.test", UserID: "u1",
				Active: memberOf[0],
				List: func(context.Context) ([]api.Organization, error) {
					calls++
					return memberOf, nil
				},
			}
			got, err := r.Resolve(context.Background(), tt.value)
			if calls != tt.wantCalls {
				t.Errorf("list calls = %d, want %d", calls, tt.wantCalls)
			}
			if tt.wantUsage {
				if !clierr.IsUsage(err) {
					t.Fatalf("want usage error, got %v", err)
				}
				var nf *NotFoundError
				if tt.wantErr != nil && !errors.As(err, &nf) {
					t.Fatalf("want NotFoundError, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != tt.wantID || got.Slug != tt.wantSlug {
				t.Fatalf("got %+v, want id %s slug %q", got, tt.wantID, tt.wantSlug)
			}
		})
	}
}

func TestResolveListErrorIsNotUsage(t *testing.T) {
	boom := errors.New("status 500")
	r := &Resolver{List: func(context.Context) ([]api.Organization, error) { return nil, boom }}
	_, err := r.Resolve(context.Background(), "acme")
	if !errors.Is(err, boom) || clierr.IsUsage(err) {
		t.Fatalf("want the list error, not a usage error: %v", err)
	}
}

func TestResolveCachesForADay(t *testing.T) {
	cache := &Cache{Path: filepath.Join(t.TempDir(), "orgs.json")}
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	calls := 0
	newResolver := func(user string) *Resolver {
		return &Resolver{
			AuthURL: "https://auth.test", UserID: user, Cache: cache,
			Now: func() time.Time { return now },
			List: func(context.Context) ([]api.Organization, error) {
				calls++
				return memberOf, nil
			},
		}
	}
	ctx := context.Background()
	for range 3 {
		if o, err := newResolver("u1").Resolve(ctx, "acme"); err != nil || o.ID != acmeID {
			t.Fatalf("resolve: %+v %v", o, err)
		}
	}
	if calls != 1 {
		t.Fatalf("list called %d times within the TTL, want 1", calls)
	}
	// A cached id comes back with its slug.
	if o, _ := newResolver("u1").Resolve(ctx, acmeID); o.Slug != "acme" {
		t.Fatalf("cached id lookup lost the slug: %+v", o)
	}
	// Another account never sees u1's cache.
	if _, err := newResolver("u2").Resolve(ctx, "acme"); err != nil || calls != 2 {
		t.Fatalf("cache leaked across users (calls %d, err %v)", calls, err)
	}
	now = now.Add(CacheTTL + time.Minute)
	if _, err := newResolver("u1").Resolve(ctx, "acme"); err != nil || calls != 3 {
		t.Fatalf("expired cache was reused (calls %d, err %v)", calls, err)
	}
	if err := cache.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := newResolver("u1").Resolve(ctx, "acme"); err != nil || calls != 4 {
		t.Fatalf("cleared cache was reused (calls %d)", calls)
	}
}

func TestIsID(t *testing.T) {
	for v, want := range map[string]bool{
		acmeID:                                 true,
		"ACME":                                 false,
		"0b9d2b8e1c1f4c3e9a516d1c2f9a7e01":     false,
		"0b9d2b8e-1c1f-4c3e-9a51-6d1c2f9a7e0z": false,
	} {
		if IsID(v) != want {
			t.Errorf("IsID(%q) = %v", v, !want)
		}
	}
}

// Package orgs turns what the user typed for --org (an id or a slug) into
// an organization id the services accept. Services only take ids; a slug
// is looked up in the organizations the user belongs to, and the answer is
// cached for a day so repeated commands don't pay for the round trip.
package orgs

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/clierr"
)

// CacheTTL is how long a slug→id answer is reused.
const CacheTTL = 24 * time.Hour

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsID reports whether v has the shape of an organization id (a UUID).
func IsID(v string) bool { return uuidRe.MatchString(strings.TrimSpace(v)) }

// NotFoundError: the value matched none of the user's organizations.
type NotFoundError struct{ Value string }

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no organization %q among the ones you belong to. Run `norcube org list` to see them", e.Value)
}

// Find returns the organization in list whose id equals value or whose
// slug equals it, ignoring case.
func Find(list []api.Organization, value string) (api.Organization, bool) {
	value = strings.TrimSpace(value)
	for _, o := range list {
		if o.ID == value || strings.EqualFold(o.Slug, value) {
			return o, true
		}
	}
	return api.Organization{}, false
}

// Resolver resolves --org values for one signed-in user.
type Resolver struct {
	// Profile, AuthURL and UserID scope the cache, so slugs never leak
	// between profiles, environments or accounts.
	Profile string
	AuthURL string
	UserID  string
	// Active is the persisted active organization; a value naming it is
	// answered without a network call.
	Active api.Organization
	// List fetches the user's organizations.
	List func(ctx context.Context) ([]api.Organization, error)
	// Cache may be nil (no caching).
	Cache *Cache
	// Now is replaced in tests.
	Now func() time.Time
}

// Resolve returns the organization value names. An id is returned as is,
// with slug and name filled in when they are known locally; the server
// checks membership when the token is minted. An unknown slug is a usage
// error (exit 2).
func (r *Resolver) Resolve(ctx context.Context, value string) (api.Organization, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return api.Organization{}, clierr.Usage(fmt.Errorf("--org must not be empty"))
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if r.Active.ID != "" && (r.Active.ID == value || strings.EqualFold(r.Active.Slug, value)) {
		return r.Active, nil
	}
	scope := ScopeKey(r.Profile, r.AuthURL, r.UserID)
	if IsID(value) {
		if o, ok := r.Cache.lookup(scope, value, now()); ok {
			return o, nil
		}
		return api.Organization{ID: value}, nil
	}
	if o, ok := r.Cache.lookup(scope, value, now()); ok {
		return o, nil
	}
	if r.List == nil {
		return api.Organization{}, fmt.Errorf("can't look up organization %q: no organization list available", value)
	}
	list, err := r.List(ctx)
	if err != nil {
		return api.Organization{}, fmt.Errorf("look up organization %q: %w", value, err)
	}
	r.Cache.store(scope, list, now())
	if o, ok := Find(list, value); ok {
		return o, nil
	}
	return api.Organization{}, clierr.Usage(&NotFoundError{Value: value})
}

// ScopeKey is the cache key of one profile's organizations.
func ScopeKey(profile, authURL, userID string) string {
	return profile + "|" + authURL + "|" + userID
}

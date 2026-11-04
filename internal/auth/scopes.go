package auth

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Scopes of a CLI sign-in. A sign-in has full access (everything the person
// can do in the dashboard) unless `login --scopes` named a subset. Norcube
// Auth always adds org.read (the account and its organizations) and keeps
// offline_access (the refresh token); the person's own permissions apply on
// every call either way.
const (
	ScopeFull          = "full"
	ScopeOrgRead       = "org.read"
	ScopeOfflineAccess = "offline_access"
)

// LoginScope is a scope `login --scopes` accepts.
type LoginScope struct {
	Name        string
	Description string
}

// LoginScopes mirrors the scope catalog of Norcube Auth for CLI sign-ins,
// minus the two scopes every sign-in carries anyway (org.read,
// offline_access). The server is the authority: it refuses an unknown
// scope with invalid_scope. A well-formed scope missing here (one Norcube
// Auth added after this release) is still sent, so it only misses the
// description and completion.
var LoginScopes = []LoginScope{
	{ScopeFull, "Everything you can do in the dashboard (the default)"},
	{"backup.read", "View backups: data sources, backup jobs, restore checks and policies"},
	{"backup.write", "Run backups, plus everything backup.read allows"},
	{"backup.verify", "Run restore checks, plus everything backup.read allows"},
	{"domainradar.check", "Run single and bulk DomainRadar checks and read the results"},
	{"langsync.read", "View namespaces, terms, translations, languages and the glossary"},
	{"langsync.sync", "Run `langsync sync`: push source terms and pull translations"},
	{"langsync.write", "Add and edit terms and translations, translate missing values"},
	{"billing.read", "View billing status, usage, invoices and the subscription"},
	{"cms.read", "View CMS sites, content, schema and media"},
	{"cms.write", "Edit CMS content and media, plus everything cms.read allows"},
	{"monitor.read", "View monitors, check results, uptime, incidents and alert channels"},
	{"monitor.check", "Run a monitor's check now, plus everything monitor.read allows"},
	{"monitor.write", "Create, change and delete monitors and acknowledge incidents, plus everything monitor.read allows"},
	{"monitor.channels", "Create, change and delete alert channels and send test alerts, plus everything monitor.read allows"},
}

// scopeShape is what a Norcube scope looks like: product or product.action.
var scopeShape = regexp.MustCompile(`^[a-z]+(\.[a-z]+)?$`)

// LoginScopeNames lists the scopes `login --scopes` accepts.
func LoginScopeNames() []string {
	out := make([]string, 0, len(LoginScopes))
	for _, s := range LoginScopes {
		out = append(out, s.Name)
	}
	return out
}

// ParseLoginScopes validates the scopes given to `login --scopes` and
// returns the ones to ask for: the known ones in catalog order, then the
// well-formed ones this release doesn't know, as given (Norcube Auth
// decides on those). Nothing means full. org.read and offline_access are
// dropped (every sign-in carries them), and full absorbs the rest. A
// malformed scope is an error naming the valid ones.
func ParseLoginScopes(values []string) ([]string, error) {
	want := map[string]bool{}
	var unknown []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		switch {
		case v == "":
			continue
		case v == ScopeOrgRead || v == ScopeOfflineAccess:
			// Implicit; asking for them changes nothing.
			want[v] = true
			continue
		case !slices.Contains(LoginScopeNames(), v):
			if !scopeShape.MatchString(v) {
				return nil, fmt.Errorf("invalid scope %q; valid scopes: %s", v, strings.Join(LoginScopeNames(), ", "))
			}
			if !slices.Contains(unknown, v) {
				unknown = append(unknown, v)
			}
		}
		want[v] = true
	}
	if len(want) == 0 {
		return []string{ScopeFull}, nil
	}
	if want[ScopeFull] {
		return []string{ScopeFull}, nil
	}
	var out []string
	for _, s := range LoginScopes {
		if want[s.Name] {
			out = append(out, s.Name)
		}
	}
	out = append(out, unknown...)
	if len(out) == 0 {
		return nil, fmt.Errorf("--scopes needs at least one scope besides %s and %s, which every sign-in has; valid scopes: %s",
			ScopeOrgRead, ScopeOfflineAccess, strings.Join(LoginScopeNames(), ", "))
	}
	return out, nil
}

// requestScope is the scope parameter of a sign-in asking for scopes:
// the scopes plus offline_access, so the CLI gets a refresh token.
func requestScope(scopes []string) string {
	if len(scopes) == 0 {
		scopes = []string{ScopeFull}
	}
	return strings.Join(append(slices.Clone(scopes), ScopeOfflineAccess), " ")
}

// ScopesFull reports whether a sign-in's granted scopes include full
// access. A sign-in without recorded scopes (the older sign-in) has full
// access too.
func ScopesFull(granted []string) bool {
	return len(granted) == 0 || slices.Contains(granted, ScopeFull)
}

// LimitedScopes are a scoped sign-in's scopes as the person asked for
// them: without the implicit org.read and offline_access. Empty for a
// full sign-in.
func LimitedScopes(granted []string) []string {
	if ScopesFull(granted) {
		return nil
	}
	var out []string
	for _, s := range granted {
		if s != ScopeOrgRead && s != ScopeOfflineAccess {
			out = append(out, s)
		}
	}
	return out
}

// DescribeScopes reads like "full" or "backup.read, backup.write".
func DescribeScopes(granted []string) string {
	if ScopesFull(granted) {
		return ScopeFull
	}
	limited := LimitedScopes(granted)
	if len(limited) == 0 {
		return ScopeOrgRead
	}
	return strings.Join(limited, ", ")
}

// parseScope splits a space-separated OAuth scope parameter.
func parseScope(s string) []string {
	return strings.Fields(s)
}

package langsync

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/norcubeplatform/cli/internal/api/apierror"
	"github.com/norcubeplatform/cli/internal/api/langsync"
)

// namespaceAccessError turns a 403/404 from a namespace-scoped endpoint
// into an actionable message. Three near-identical causes produce the
// same opaque "Invalid namespace" from the server, so we list them all
// with concrete next-step commands instead of guessing.
//
// activeOrg is the slug of the org the CLI thinks it's operating in (we
// already loaded it to build the token); ns is the namespace the user
// asked for. Both end up in the message so the user can immediately tell
// whether the mismatch is on the org or namespace side.
func namespaceAccessError(activeOrg, ns string) error {
	orgClause := "your active organization"
	if activeOrg != "" {
		orgClause = fmt.Sprintf("the active organization (%q)", activeOrg)
	}
	//nolint:staticcheck // multi-line guidance message; the punctuation is deliberate
	return fmt.Errorf(`namespace %q is not accessible to %s.

This usually means one of:
  1. You're signed into the wrong org. Check with %s, then switch via %s.
  2. The namespace doesn't exist yet — create it with %s, or list what's available with %s.
  3. The namespace name is misspelled (it's the slug, case-sensitive).`,
		ns, orgClause,
		"`norcube whoami`", "`norcube org switch`",
		"`norcube langsync namespace create <name> --default-language <code>`",
		"`norcube langsync namespace list`")
}

// isNamespaceForbidden returns true when the server's typed error looks
// like a namespace-access denial. We match on the message rather than
// solely on status because 403 can also surface for other reasons
// (e.g. revoked org membership), and we don't want to claim "namespace
// problem" when it isn't.
func isNamespaceForbidden(e *langsync.ResponseAPIError) bool {
	if e == nil {
		return false
	}
	if e.Type == "FORBIDDEN" || e.Type == "NOT_FOUND" {
		// Be lenient about case + wording — covers "Invalid namespace",
		// "namespace not found", "no access to namespace", etc.
		return strings.Contains(strings.ToLower(e.Msg), "namespace")
	}
	return false
}

// apiError renders a non-2xx langsync response via the shared formatter
// (see internal/api/apierror). Typed bodies win over the raw body.
func apiError(resp *http.Response, body []byte, typed ...*langsync.ResponseAPIError) error {
	conv := make([]*apierror.Typed, 0, len(typed))
	for _, t := range typed {
		conv = append(conv, typedOf(t))
	}
	return apierror.Format("langsync", resp, body, conv...)
}

func typedOf(e *langsync.ResponseAPIError) *apierror.Typed {
	if e == nil {
		return nil
	}
	return &apierror.Typed{Msg: e.Msg, Type: string(e.Type)}
}

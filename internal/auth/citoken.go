package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/registry"
	"github.com/norcubeplatform/cli/internal/transport"
)

// CI tokens are organization-scoped secrets for automation, created under
// IAM › CI tokens in the dashboard or with `norcube auth ci-token create`.
// They act as a service principal of one organization, not as a person,
// and are exchanged at Norcube Auth for short-lived access tokens, one per
// service. Nothing is stored: the CLI keeps the access tokens in memory for
// the life of the process.
//
// The format is `nrc_ci_` + 32 base62 characters + a 6-character base62
// CRC32 (IEEE) of those 32, the scheme GitHub uses so secret scanners can
// recognize leaks and typos are caught before any request.
const (
	CITokenPrefix         = "nrc_ci_"
	ciTokenRandomLength   = 32
	ciTokenChecksumLength = 6
	ciTokenBase62         = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// IsCIToken reports whether s has the CI token prefix. It says nothing
// about validity; see ValidateCIToken.
func IsCIToken(s string) bool { return strings.HasPrefix(s, CITokenPrefix) }

// ErrMalformedCIToken: the value starts like a CI token but its length,
// characters or checksum are wrong, almost always a copy-paste mistake.
var ErrMalformedCIToken = errors.New("malformed CI token")

// ValidateCIToken checks a CI token's shape and checksum locally.
func ValidateCIToken(s string) error {
	body, ok := strings.CutPrefix(s, CITokenPrefix)
	if !ok || len(body) != ciTokenRandomLength+ciTokenChecksumLength {
		return ErrMalformedCIToken
	}
	for i := 0; i < len(body); i++ {
		if strings.IndexByte(ciTokenBase62, body[i]) < 0 {
			return ErrMalformedCIToken
		}
	}
	if ciTokenChecksum(body[:ciTokenRandomLength]) != body[ciTokenRandomLength:] {
		return ErrMalformedCIToken
	}
	return nil
}

// ciTokenChecksum is the CRC32 (IEEE) of s in base62, left-padded with
// zeros to ciTokenChecksumLength characters.
func ciTokenChecksum(s string) string {
	n := crc32.ChecksumIEEE([]byte(s))
	out := make([]byte, ciTokenChecksumLength)
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = ciTokenBase62[n%62]
		n /= 62
	}
	return string(out)
}

// CIGrant is what an exchange returns: an access token for one service
// plus what the CI token is.
type CIGrant struct {
	AccessToken string
	ExpiresAt   time.Time
	Audience    string
	// Scopes the access token carries for this service.
	Scopes []string

	OrgID   string
	OrgSlug string
	OrgName string

	TokenID        string
	TokenName      string
	TokenExpiresAt time.Time
}

// CIExchanger trades a CI token for access tokens at Norcube Auth
// (POST /auth/ci/token) and caches them per service in memory until
// shortly before they expire. It is safe for concurrent use.
type CIExchanger struct {
	APIBase string
	HTTP    *http.Client
	Secret  string

	mu     sync.Mutex
	grants map[string]*CIGrant
	now    func() time.Time
}

// NewCIExchanger returns an exchanger for secret against Norcube Auth at
// apiBase.
func NewCIExchanger(apiBase, secret string) *CIExchanger {
	return &CIExchanger{APIBase: apiBase, Secret: secret, HTTP: transport.Default()}
}

func (e *CIExchanger) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// Token returns an access token for audience.
func (e *CIExchanger) Token(ctx context.Context, audience string) (string, error) {
	g, err := e.Grant(ctx, audience)
	if err != nil {
		return "", err
	}
	return g.AccessToken, nil
}

// Grant returns a cached grant for audience with more than expiryMargin
// of life left, or exchanges the CI token for a new one. Exchanges are
// serialized, so concurrent requests for a service share one.
func (e *CIExchanger) Grant(ctx context.Context, audience string) (*CIGrant, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if g, ok := e.grants[audience]; ok && g.ExpiresAt.Sub(e.clock()) > expiryMargin {
		return g, nil
	}
	g, err := e.exchange(ctx, audience)
	if err != nil {
		return nil, err
	}
	if e.grants == nil {
		e.grants = map[string]*CIGrant{}
	}
	e.grants[audience] = g
	return g, nil
}

type ciExchangeResponse struct {
	AccessToken      string    `json:"access_token"`
	ExpiresIn        int64     `json:"expires_in"`
	Scope            string    `json:"scope"`
	Audience         string    `json:"audience"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationSlug string    `json:"organization_slug"`
	OrganizationName string    `json:"organization_name"`
	CITokenID        string    `json:"ci_token_id"`
	CITokenName      string    `json:"ci_token_name"`
	CITokenExpiresAt time.Time `json:"ci_token_expires_at"`
}

const (
	ciGrantType        = "urn:ietf:params:oauth:grant-type:token-exchange"
	ciSubjectTokenType = "urn:norcube:params:oauth:token-type:ci-token" // #nosec G101 -- a token type URN, not a credential
)

func (e *CIExchanger) exchange(ctx context.Context, audience string) (*CIGrant, error) {
	form := url.Values{
		"grant_type":         {ciGrantType},
		"subject_token_type": {ciSubjectTokenType},
		"audience":           {audience},
	}
	endpoint := e.APIBase + "/auth/ci/token"
	// #nosec G704 -- APIBase is the Norcube Auth URL the user configured.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// The secret travels in the Authorization header, never in the body,
	// so it can't end up in a form-logging proxy.
	req.Header.Set("Authorization", "Bearer "+e.Secret)
	// An exchange changes nothing but the token's last-used time, so it is
	// safe to repeat: let the shared transport retry it on 429 (honoring
	// Retry-After), 5xx and network errors like any idempotent request.
	req.Header.Set(transport.IdempotencyKeyHeader, newIdempotencyKey())

	received := e.clock()
	resp, err := e.HTTP.Do(req) // #nosec G704 -- see above
	if err != nil {
		return nil, fmt.Errorf("exchange CI token: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, ciExchangeError(resp, raw, endpoint, audience)
	}
	var out ciExchangeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode CI token exchange: %w", err)
	}
	if out.AccessToken == "" {
		return nil, errors.New("CI token exchange returned no access token")
	}
	return &CIGrant{
		AccessToken:    out.AccessToken,
		ExpiresAt:      received.Add(time.Duration(out.ExpiresIn) * time.Second),
		Audience:       audience,
		Scopes:         strings.Fields(out.Scope),
		OrgID:          out.OrganizationID,
		OrgSlug:        out.OrganizationSlug,
		OrgName:        out.OrganizationName,
		TokenID:        out.CITokenID,
		TokenName:      out.CITokenName,
		TokenExpiresAt: out.CITokenExpiresAt,
	}, nil
}

// CIScopeAudience is set as clierr.Error.Details on an insufficient-scope
// refusal, naming the service the CI token has no scope for.
type CIScopeAudience struct {
	Audience string `json:"audience" yaml:"audience"`
}

// ciExchangeError maps a refused exchange to an error with the right exit
// code and a message that makes sense in a CI log.
func ciExchangeError(resp *http.Response, raw []byte, endpoint, audience string) error {
	var body struct {
		Type string `json:"type"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &body)
	e := &clierr.Error{
		HTTPStatus: resp.StatusCode,
		Service:    "auth",
		Method:     http.MethodPost,
		URL:        endpoint,
		Type:       body.Type,
		RequestID:  resp.Header.Get("X-Request-Id"),
	}
	detail := body.Msg
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		e.Code = clierr.CodeLoginRequired
		e.Message = "the CI token is invalid, expired or revoked"
		e.Hint = "Create a new token with `norcube auth ci-token create` or under IAM › CI tokens in the dashboard, and update NORCUBE_TOKEN."
	case http.StatusForbidden:
		e.Code = clierr.CodeInsufficientScope
		e.Message = fmt.Sprintf("the CI token has no scope for %s", audienceLabel(audience))
		e.Details = CIScopeAudience{Audience: audience}
		e.Hint = "Create a token with the scope this command needs; `norcube auth ci-token scopes` lists them."
	case http.StatusTooManyRequests:
		e.Code = clierr.CodeRateLimited
		e.Message = "CI token exchanges are rate limited; try again shortly"
	default:
		// Only here is the server's text news to the reader; for the
		// refusals above our sentence already says it.
		e.Code = clierr.CodeForStatus(resp.StatusCode, body.Type)
		e.Message = "CI token exchange failed"
		if detail != "" {
			e.Message += ": " + detail
		}
	}
	return e
}

// audienceLabel names a service audience the way users know it, from the
// service registry.
func audienceLabel(audience string) string {
	for _, svc := range registry.All() {
		if svc.Audience == audience {
			return svc.Name
		}
	}
	if audience == AudienceCMS { // CI tokens can hold CMS scopes; the CLI has no CMS commands yet
		return "Norcube CMS"
	}
	return audience
}

func newIdempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Audience names recognized by the auth service. These match the constants in
// apps/auth/internal/handler/user/user.go.
const (
	AudienceAuth        = "auth"
	AudienceBilling     = "billing"
	AudienceSnapDB      = "snapdb-api"
	AudienceLangsync    = "langsync-api"
	AudienceDomainradar = "domainradar"
	AudiencePrompthub   = "prompthub"
)

// TokenSource returns a valid Bearer access token for a (audience, organization)
// pair, refreshing it via /oauth/token when necessary. It is the single place
// the rest of the CLI should ask for "give me a token I can use right now."
type TokenSource struct {
	APIBase  string
	Audience string
	OrgID    string
	HTTP     *http.Client
}

func NewTokenSource(apiBase, audience, orgID string) *TokenSource {
	return &TokenSource{
		APIBase:  apiBase,
		Audience: audience,
		OrgID:    orgID,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Token returns a valid access token, minting one via /oauth/token if no
// cached token exists or the cached token is close to expiring.
//
// Side effect: when /oauth/token returns a rotated refresh token, the
// keyring-stored refresh token is updated in place. This keeps the CLI
// logged in indefinitely as long as the user runs `nrc` at least once
// per refresh-token TTL — the rotation pushes the expiry forward on
// each exchange. The user only sees `nrc login --force` again if they
// stay completely inactive for the full TTL window.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	cached, _ := LoadAccessToken(t.APIBase, t.Audience, t.OrgID)
	if cached != "" && !isExpiringSoon(cached) {
		return cached, nil
	}

	refresh, err := LoadRefreshToken(t.APIBase)
	if err != nil {
		return "", err
	}

	access, rotated, err := t.exchangeRefresh(ctx, refresh)
	if err != nil {
		return "", err
	}
	if rotated != "" {
		if err := SaveRefreshToken(t.APIBase, rotated); err != nil {
			// Failing to persist the rotated refresh token means the
			// next /oauth/token call will fail (the keyring still
			// has the now-revoked old value). Surface it loudly so
			// the user can re-login rather than chasing a phantom
			// 401 later.
			return "", fmt.Errorf("persist rotated refresh token: %w", err)
		}
	}
	if err := SaveAccessToken(t.APIBase, t.Audience, t.OrgID, access); err != nil {
		// Cache failure is non-fatal — we still got a usable token.
	}
	return access, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	// RefreshToken is non-empty when the auth service rotated the
	// refresh token on this exchange. When present the CLI MUST
	// replace the keyring-stored refresh token with this value —
	// the previously-stored one has already been revoked
	// server-side and will fail on the next /oauth/token call.
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type apiError struct {
	Type string `json:"type"`
	Msg  string `json:"msg"`
}

// exchangeRefresh calls POST /oauth/token (form-encoded). The auth service
// reads the refresh token from a cookie when called from the browser; for the
// CLI we send it in the form body via a Cookie header instead.
//
// Returns (accessToken, rotatedRefreshToken, error). When the auth service
// rotates the refresh token on this call the rotated value is returned via
// the second result and the caller MUST persist it before the next
// /oauth/token call (the presented refresh token has been revoked
// server-side). Empty rotated string means the server didn't rotate
// (legacy server, or a flow that doesn't issue refresh tokens).
func (t *TokenSource) exchangeRefresh(ctx context.Context, refresh string) (string, string, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("audience", t.Audience)
	if t.OrgID != "" {
		form.Set("organization_id", t.OrgID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.APIBase+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The handler reads the refresh token from the configured cookie name.
	req.AddCookie(&http.Cookie{Name: "jds_refresh_token", Value: refresh})

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("oauth/token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Msg != "" {
			return "", "", fmt.Errorf("oauth/token failed (%d): %s", resp.StatusCode, apiErr.Msg)
		}
		return "", "", fmt.Errorf("oauth/token failed (%d)", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", "", fmt.Errorf("decode oauth/token: %w", err)
	}
	if tr.AccessToken == "" {
		return "", "", errors.New("oauth/token returned empty access token")
	}
	return tr.AccessToken, tr.RefreshToken, nil
}

// isExpiringSoon decodes a JWT's exp claim without verifying the signature
// (the server validates it on the next request anyway) and returns true if
// the token has less than 30 seconds of life left.
func isExpiringSoon(jwt string) bool {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return true
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return true
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return true
	}
	if claims.Exp == 0 {
		return true
	}
	return time.Until(time.Unix(claims.Exp, 0)) < 30*time.Second
}

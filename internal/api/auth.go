package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/norcubeplatform/cli/internal/api/apierror"
	"github.com/norcubeplatform/cli/internal/transport"
)

// AuthClient talks to the auth service (/auth/me, /organizations, ...).
// It expects the caller to provide a valid Bearer token via Authorization.
type AuthClient struct {
	BaseURL string
	HTTP    *http.Client
	Token   func(ctx context.Context) (string, error)
}

func NewAuthClient(baseURL string, token func(ctx context.Context) (string, error)) *AuthClient {
	return &AuthClient{
		BaseURL: baseURL,
		HTTP:    transport.Default(),
		Token:   token,
	}
}

type Me struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatarUrl"`
}

type Organization struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug"`
	LogoURL   *string `json:"logo_url"`
	IsDefault bool    `json:"is_default"`
}

func (c *AuthClient) GetMe(ctx context.Context) (*Me, error) {
	var out Me
	if err := c.do(ctx, http.MethodGet, "/auth/me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *AuthClient) ListOrganizations(ctx context.Context) ([]Organization, error) {
	var out []Organization
	if err := c.do(ctx, http.MethodGet, "/organizations", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateOrganization creates a new org with the given name. slug is
// optional — when nil or empty the server derives one from the name.
// The caller becomes the owner. Returns the new org's id, slug, and
// canonicalised name.
//
// The server returns the new org wrapped in {"organization": {...}}
// — we unwrap so the caller gets the same Organization shape that
// ListOrganizations hands back.
func (c *AuthClient) CreateOrganization(ctx context.Context, name string, slug *string) (*Organization, error) {
	payload := struct {
		Name string  `json:"name"`
		Slug *string `json:"slug,omitempty"`
	}{Name: name, Slug: slug}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Organization Organization `json:"organization"`
	}
	if err := c.do(ctx, http.MethodPost, "/organizations", bytes.NewReader(body), &resp); err != nil {
		return nil, err
	}
	return &resp.Organization, nil
}

// Session is one signed-in CLI session of the caller (GET /auth/me/sessions).
type Session struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"`
	Hostname   *string       `json:"hostname"`
	Client     SessionClient `json:"client"`
	IPAddress  *string       `json:"ip_address"`
	CreatedAt  time.Time     `json:"created_at"`
	LastUsedAt time.Time     `json:"last_used_at"`
	ExpiresAt  time.Time     `json:"expires_at"`
	// Current marks the session the request's own token belongs to.
	Current bool `json:"current"`
}

// SessionClient is what the session's CLI reported about itself; fields
// are nil for CLIs that didn't identify themselves.
type SessionClient struct {
	Name    *string `json:"name"`
	Version *string `json:"version"`
	OS      *string `json:"os"`
	Arch    *string `json:"arch"`
}

// ListSessions lists the caller's live CLI sessions, most recently used
// first.
func (c *AuthClient) ListSessions(ctx context.Context) ([]Session, error) {
	var out []Session
	if err := c.do(ctx, http.MethodGet, "/auth/me/sessions", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeSession ends one of the caller's sessions. Idempotent for the
// caller's own sessions; an unknown id is a not-found error.
func (c *AuthClient) RevokeSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/auth/me/sessions/"+url.PathEscape(id), nil, nil)
}

// RevokeOtherSessions ends every session of the caller except the one the
// request's own token belongs to, and returns how many it ended.
func (c *AuthClient) RevokeOtherSessions(ctx context.Context) (int64, error) {
	var out struct {
		Revoked int64 `json:"revoked"`
	}
	if err := c.do(ctx, http.MethodPost, "/auth/me/sessions/revoke-others", nil, &out); err != nil {
		return 0, err
	}
	return out.Revoked, nil
}

// CIToken is an organization's CI token as Norcube Auth lists it. The
// secret itself is only ever returned once, by CreateCIToken.
type CIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Preview    string     `json:"preview"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  *string    `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP *string    `json:"last_used_ip"`
	RevokedAt  *time.Time `json:"revoked_at"`
	// Status is active, expired or revoked.
	Status string `json:"status"`
}

// CITokenScope is a scope a CI token can carry, and whether the caller's
// role allows granting it.
type CITokenScope struct {
	Scope       string `json:"scope"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Product     string `json:"product"`
	Write       bool   `json:"write"`
	Available   bool   `json:"available"`
}

// CreatedCIToken is a new CI token plus its secret, shown once.
type CreatedCIToken struct {
	Token  CIToken `json:"token"`
	Secret string  `json:"secret"`
}

// ListCITokens lists the organization's CI tokens (the organization of the
// client's token).
func (c *AuthClient) ListCITokens(ctx context.Context) ([]CIToken, error) {
	var out []CIToken
	if err := c.do(ctx, http.MethodGet, "/iam/ci-tokens", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListCITokenScopes lists every CI token scope and whether the caller may
// grant it.
func (c *AuthClient) ListCITokenScopes(ctx context.Context) ([]CITokenScope, error) {
	var out []CITokenScope
	if err := c.do(ctx, http.MethodGet, "/iam/ci-tokens/scopes", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateCIToken creates a CI token in the organization and returns it with
// its secret.
func (c *AuthClient) CreateCIToken(ctx context.Context, name string, scopes []string, expiresAt time.Time) (*CreatedCIToken, error) {
	body, err := json.Marshal(struct {
		Name      string    `json:"name"`
		Scopes    []string  `json:"scopes"`
		ExpiresAt time.Time `json:"expires_at"`
	}{name, scopes, expiresAt.UTC()})
	if err != nil {
		return nil, err
	}
	var out CreatedCIToken
	if err := c.do(ctx, http.MethodPost, "/iam/ci-tokens", bytes.NewReader(body), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeCIToken revokes a CI token; revoking a revoked token is a no-op.
func (c *AuthClient) RevokeCIToken(ctx context.Context, id string) (*CIToken, error) {
	var out CIToken
	if err := c.do(ctx, http.MethodDelete, "/iam/ci-tokens/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *AuthClient) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	token, err := c.Token(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return apierror.Format("auth", resp, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}

// ErrUnauthorized is returned when the API rejects our token. Higher-level
// commands can suggest re-running `norcube login`.
var ErrUnauthorized = errors.New("unauthorized — your session may have expired, run `norcube login`")

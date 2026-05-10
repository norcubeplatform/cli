package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
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
		HTTP:    &http.Client{Timeout: 30 * time.Second},
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

type apiError struct {
	Type string `json:"type"`
	Msg  string `json:"msg"`
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
		var apiErr apiError
		_ = json.Unmarshal(raw, &apiErr)
		if apiErr.Msg != "" {
			return fmt.Errorf("%s %s: %s (%d)", method, path, apiErr.Msg, resp.StatusCode)
		}
		return fmt.Errorf("%s %s: status %d", method, path, resp.StatusCode)
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

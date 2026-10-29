package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/norcubeplatform/cli/internal/transport"
)

// SignOutTimeout bounds the server call made by `norcube logout`: logging
// out must not hang on a slow or unreachable server.
const SignOutTimeout = 10 * time.Second

// EndServerSession ends the CLI session behind refreshToken on Norcube Auth
// (POST /auth/cli/sign-out), so its refresh token stops working everywhere
// at once. The endpoint answers 204 for a live, an already ended or an
// unknown token alike; anything else is an error. Callers hold the
// profile's refresh lock, so no refresh can rotate the token mid-call.
func EndServerSession(ctx context.Context, client *http.Client, authURL, refreshToken string) error {
	if client == nil {
		client = transport.Default()
	}
	ctx, cancel := context.WithTimeout(ctx, SignOutTimeout)
	defer cancel()

	form := url.Values{"refresh_token": {refreshToken}}
	// #nosec G704 -- authURL is the Norcube Auth URL of the user's profile.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(authURL, "/")+"/auth/cli/sign-out",
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	// The token goes in the form body only: a cookie would make the
	// endpoint end the browser session instead.
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req) // #nosec G704 -- see above
	if err != nil {
		return fmt.Errorf("auth/cli/sign-out: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("auth/cli/sign-out: status %d", resp.StatusCode)
	}
	return nil
}

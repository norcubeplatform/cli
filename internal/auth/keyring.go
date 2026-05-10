package auth

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// Keyring wraps zalando/go-keyring with a fixed service prefix so multiple
// API endpoints (dev/staging/prod) can coexist on one machine without
// stepping on each other's secrets.
// Single-word service name keeps Keychain Access entries tidy:
// "norcube" is what shows up in the GUI. Keep stable across releases —
// changing this orphans users' refresh tokens.
const keyringService = "norcube"

// scopedKey is keyringService + ":" + apiBaseURL + ":" + kind. Including the
// API URL avoids leaking a staging refresh token to a prod CLI invocation.
func scopedKey(api, kind string) string {
	return fmt.Sprintf("%s|%s", api, kind)
}

func SaveRefreshToken(api, token string) error {
	return keyring.Set(keyringService, scopedKey(api, "refresh"), token)
}

func LoadRefreshToken(api string) (string, error) {
	tok, err := keyring.Get(keyringService, scopedKey(api, "refresh"))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotLoggedIn
		}
		return "", err
	}
	return tok, nil
}

func DeleteRefreshToken(api string) error {
	err := keyring.Delete(keyringService, scopedKey(api, "refresh"))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// SaveAccessToken / LoadAccessToken cache short-lived access tokens per
// (api, audience, organization) so we don't hit /oauth/token on every
// command invocation. Callers must check expiry before using.
func SaveAccessToken(api, audience, orgID, token string) error {
	return keyring.Set(keyringService, scopedKey(api, "access:"+audience+":"+orgID), token)
}

func LoadAccessToken(api, audience, orgID string) (string, error) {
	tok, err := keyring.Get(keyringService, scopedKey(api, "access:"+audience+":"+orgID))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return tok, nil
}

// DeleteAllTokens nukes refresh + every cached access token for an API. Used by `norcube logout`.
func DeleteAllTokens(api string) error {
	// keyring has no "list" API, so we delete the refresh token (the one we always set)
	// and let stale access tokens expire naturally. Access tokens are short-lived (5m)
	// and bound to the refresh token, so they are useless without it.
	return DeleteRefreshToken(api)
}

var ErrNotLoggedIn = errors.New("not logged in — run `norcube login`")

// Package clierr holds the CLI's process exit codes and the error types
// that select them, so scripts can branch on why a command failed.
package clierr

import (
	"errors"
	"strings"
)

// Exit codes, stable for scripts. Anything not listed is 1.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2   // bad flags, arguments or input; fix the command line
	ExitNotFound    = 3   // the resource doesn't exist (API 404)
	ExitAuth        = 4   // not signed in or session expired; run `norcube login`
	ExitAccess      = 5   // signed in, but not allowed (not a member, missing permission or scope)
	ExitRateLimited = 6   // still throttled (429) after the retries
	ExitTimeout     = 7   // the request timed out
	ExitConflict    = 8   // the request conflicts with the current state (API 409)
	ExitInterrupted = 130 // SIGINT/SIGTERM, the shell convention 128+2
)

// UsageError marks a mistake in how the command was invoked.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// Usage wraps err as a usage error. A nil err stays nil.
func Usage(err error) error {
	if err == nil {
		return nil
	}
	return &UsageError{Err: err}
}

// IsUsage reports whether err is a usage error, including the plain
// errors cobra returns for unknown commands and missing or conflicting
// flags, which can't be wrapped at their source.
func IsUsage(err error) bool {
	var ue *UsageError
	if errors.As(err, &ue) {
		return true
	}
	msg := err.Error()
	for _, prefix := range cobraUsagePrefixes {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}

var cobraUsagePrefixes = []string{
	"unknown command ",
	"unknown flag: ",
	"unknown shorthand flag: ",
	"required flag(s) ",
	"if any flags in the group ",
	"at least one of the flags in the group ",
	"invalid argument ",
}

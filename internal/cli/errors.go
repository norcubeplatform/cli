package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/orgs"
	"github.com/norcubeplatform/cli/internal/transport"
)

// EnvErrorFormat set to "json" makes every error a JSON object on stderr,
// whatever the output format.
const EnvErrorFormat = "NORCUBE_ERROR_FORMAT"

// selectedOutput is the resolved output format of the running command,
// set once its flags are parsed.
var selectedOutput string

// Describe turns any error the CLI returns into a *clierr.Error with a
// code, message and hint. API errors already are one; the rest are
// classified here.
func Describe(err error) *clierr.Error {
	if err == nil {
		return nil
	}
	var ce *clierr.Error
	if errors.As(err, &ce) {
		out := *ce
		if out.Code == clierr.CodeLoginRequired && out.Hint == "" {
			out.Hint = fmt.Sprintf("Run `%s` to sign in again.", LoginCommand())
		}
		if out.Message == "" {
			out.Message = err.Error()
		}
		if usingCIToken && out.Hint == "" &&
			(out.Code == clierr.CodePermissionDenied || out.Code == clierr.CodeInsufficientScope) {
			out.Hint = ciRefusalHint
		}
		return &out
	}

	e := &clierr.Error{Code: clierr.CodeError, Message: err.Error(), Err: err}
	var notFound *orgs.NotFoundError
	var timeout *transport.TimeoutError
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		e.Code, e.Message = clierr.CodeInterrupted, "interrupted"
	case errors.Is(err, auth.ErrNotLoggedIn):
		e.Code = clierr.CodeLoginRequired
		e.Message = "not signed in" + profileSuffix()
		e.Hint = fmt.Sprintf("Run `%s`.", LoginCommand())
	case errors.Is(err, auth.ErrLoginRequired):
		e.Code = clierr.CodeLoginRequired
		e.Hint = fmt.Sprintf("Run `%s` to sign in again.", LoginCommand())
	case errors.Is(err, auth.ErrOrgAccessDenied):
		e.Code = clierr.CodePermissionDenied
	case errors.As(err, &notFound):
		e.Code = clierr.CodeOrgNotFound
	case clierr.IsUsage(err):
		e.Code = clierr.CodeUsage
	case errors.As(err, &timeout), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, transport.ErrIdleTimeout),
		errors.As(err, &netErr) && netErr.Timeout():
		e.Code = clierr.CodeTimeout
	}
	return e
}

// usingCIToken is set when the running command authenticates with a CI
// token (NORCUBE_TOKEN or --token-file), so a refusal can say why.
var usingCIToken bool

// ciRefusalHint explains a refusal under a CI token: a scope may be
// missing, or the action is one no CI token can do (Backup restores and
// data source or policy changes always need a signed-in person).
const ciRefusalHint = "A CI token can only do what its scopes allow, and some actions (such as Backup restores and data source or policy changes) always need a person: run them after `norcube login`. `norcube auth ci-token scopes` lists what each scope allows."

// profileSuffix names the profile in a message when --profile or
// NORCUBE_PROFILE chose it.
func profileSuffix() string {
	if selectedProfileExplicit && selectedProfile != "" {
		return " (profile " + selectedProfile + ")"
	}
	return ""
}

// ErrorsAsJSON reports whether errors are written as JSON: with
// NORCUBE_ERROR_FORMAT=json, or when the command's output is json or yaml.
func ErrorsAsJSON() bool {
	if strings.EqualFold(os.Getenv(EnvErrorFormat), "json") {
		return true
	}
	format := selectedOutput
	if format == "" {
		// The command failed before its flags were resolved (a usage
		// error): read -o from the arguments, then NORCUBE_OUTPUT.
		format = outputFromArgs(os.Args[1:])
		if format == "" {
			format = os.Getenv("NORCUBE_OUTPUT")
		}
	}
	return format == "json" || format == "yaml"
}

func outputFromArgs(args []string) string {
	for i, a := range args {
		switch {
		case a == "--":
			return ""
		case (a == "-o" || a == "--output") && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(a, "--output="):
			return strings.TrimPrefix(a, "--output=")
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			return strings.TrimPrefix(strings.TrimPrefix(a, "-o"), "=")
		}
	}
	return ""
}

// PrintError writes err to w, as {"error": {...}} when asJSON, otherwise as
// the message and, on its own line, the hint. It returns the exit code.
func PrintError(w io.Writer, err error, asJSON bool) int {
	e := Describe(err)
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]*clierr.Error{"error": e})
		return e.ExitCode()
	}
	switch {
	case e.Code == clierr.CodeInterrupted:
		fmt.Fprintln(w, "\nInterrupted.")
	case errors.Is(err, auth.ErrLoginRequired):
		fmt.Fprintln(w, LoginHint(err))
	default:
		fmt.Fprintln(w, e.Error())
		if e.Hint != "" {
			fmt.Fprintln(w, e.Hint)
		}
	}
	return e.ExitCode()
}

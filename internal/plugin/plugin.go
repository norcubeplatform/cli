// Package plugin runs the executables that implement a service's commands outside this
// repository, such as a closed-source engine. The protocol is in
// docs/PLUGINS.md.
//
// Safety rules:
//   - only executables named in the service registry run; nothing is discovered by prefix;
//   - an executable, or the directory it is in, that other users may write is refused;
//   - before the first run the executable must answer `__plugin-info` with a matching name
//     and a supported protocol version.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Protocol is the plugin protocol version this CLI speaks.
const Protocol = 1

// InfoArg is the argument a plugin answers with its Info as JSON on stdout.
const InfoArg = "__plugin-info"

// DirEnv overrides the plugin directory.
const DirEnv = "NORCUBE_PLUGIN_DIR"

// infoTimeout bounds the protocol handshake.
const infoTimeout = 5 * time.Second

// Info is a plugin's answer to InfoArg.
type Info struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// ErrNotInstalled means the executable was found neither in the plugin directory nor on PATH.
var ErrNotInstalled = errors.New("plugin not installed")

// Dir is the directory plugins are installed into: NORCUBE_PLUGIN_DIR, else
// <user config dir>/norcube/plugins.
func Dir() (string, error) {
	if d := os.Getenv(DirEnv); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "norcube", "plugins"), nil
}

// Locate finds the executable: the plugin directory first, then PATH.
func Locate(name string) (string, error) {
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	if dir, err := Dir(); err == nil {
		p := filepath.Join(dir, file)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, checkPermissions(p)
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotInstalled, name)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return abs, checkPermissions(abs)
}

// checkPermissions refuses an executable or directory that other users can write: anyone
// who can replace it could run code as the person who types the command.
func checkPermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, p := range []string{path, filepath.Dir(path)} {
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		if st.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s is writable by other users (mode %v); refusing to run it", p, st.Mode().Perm())
		}
	}
	return nil
}

// Handshake runs `<path> __plugin-info` and checks the answer.
func Handshake(ctx context.Context, path, name string) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, InfoArg) // #nosec G204 -- path from the registry-bound lookup
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return Info{}, fmt.Errorf("%s %s: %w", name, InfoArg, err)
	}
	var info Info
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		return Info{}, fmt.Errorf("%s %s: invalid answer: %w", name, InfoArg, err)
	}
	if info.Name != name {
		return info, fmt.Errorf("%s answered as %q", name, info.Name)
	}
	if info.Protocol != Protocol {
		return info, fmt.Errorf("%s speaks plugin protocol %d, this CLI speaks %d: update the one that is older", name, info.Protocol, Protocol)
	}
	return info, nil
}

// Run executes the plugin with args and the CLI's context in env, wiring the standard
// streams, and returns its exit code.
func Run(ctx context.Context, path string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- path from the registry-bound lookup
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		return exit.ExitCode(), nil
	default:
		return 0, err
	}
}

package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/plugin"
	"github.com/norcubeplatform/cli/internal/registry"
)

// newPluginCmds returns a command for every registry service implemented by a plugin.
func newPluginCmds() []*cobra.Command {
	var out []*cobra.Command
	for _, s := range registry.All() {
		if s.Plugin != "" {
			out = append(out, newPluginCmd(s))
		}
	}
	return out
}

func newPluginCmd(s registry.Service) *cobra.Command {
	cmd := &cobra.Command{
		Use:     s.Command + " [args...]",
		Aliases: s.Aliases,
		Short:   fmt.Sprintf("Norcube %s (plugin %s)", s.Name, s.Plugin),
		Long: fmt.Sprintf(`Runs the Norcube %[1]s plugin, %[2]s, with the arguments that follow.

The plugin is installed separately: put the %[2]s executable in the plugin
directory (NORCUBE_PLUGIN_DIR, by default <config dir>/norcube/plugins) or on
PATH. The CLI only ever runs plugins it knows by name, refuses an executable
that other users can write, and checks the plugin protocol version first.
The plugin's own help: norcube %[3]s --help.`, s.Name, s.Plugin, s.Command),
		Example:            fmt.Sprintf("  norcube %[1]s --help\n  norcube %[1]s version", s.Command),
		Annotations:        cmdutil.Local(),
		DisableFlagParsing: true, // every argument belongs to the plugin
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			path, err := plugin.Locate(s.Plugin)
			if errors.Is(err, plugin.ErrNotInstalled) {
				dir, _ := plugin.Dir()
				return &clierr.Error{
					Code:    clierr.CodeNotFound,
					Message: fmt.Sprintf("the %s plugin (%s) is not installed", s.Name, s.Plugin),
					Hint:    fmt.Sprintf("Install %s into %s or anywhere on PATH.", s.Plugin, dir),
					Err:     err,
				}
			}
			if err != nil {
				return &clierr.Error{Code: clierr.CodeError, Message: err.Error(), Err: err}
			}
			if _, err := plugin.Handshake(ctx, path, s.Plugin); err != nil {
				return &clierr.Error{Code: clierr.CodeError, Message: err.Error(), Err: err}
			}
			code, err := plugin.Run(ctx, path, args, pluginEnv(cmd), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return &clierr.Error{Code: clierr.CodeError, Message: fmt.Sprintf("%s: %v", s.Plugin, err), Err: err}
			}
			if code != 0 {
				return clierr.PassthroughExit{Code: code}
			}
			return nil
		},
	}
	return cmd
}

// pluginEnv is the CLI context a plugin receives (docs/PLUGINS.md). Credentials are never
// passed: a plugin that needs a token runs `$NRC_BIN auth token --audience <aud>`.
func pluginEnv(cmd *cobra.Command) []string {
	env := []string{fmt.Sprintf("NRC_PLUGIN_PROTOCOL=%d", plugin.Protocol)}
	if exe, err := os.Executable(); err == nil {
		env = append(env, "NRC_BIN="+exe)
	}
	if flags := clictx.Get(cmd); flags != nil {
		add := func(k, v string) {
			if v != "" {
				env = append(env, k+"="+v)
			}
		}
		add("NRC_PROFILE", flags.Profile)
		add("NRC_ORG", flags.OrgOverride)
		add("NRC_OUTPUT", flags.Output)
		if flags.Debug {
			env = append(env, "NRC_DEBUG=1")
		}
	}
	return env
}

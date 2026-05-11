package langsync

import (
	"time"

	"github.com/spf13/cobra"
)

// newPullCmd is `nrc langsync pull` — the discoverable verb for
// "give me what's on the server." Under the hood it's a sync with
// strategy=server (skip push entirely). All namespaces are pulled
// in parallel via the same fan-out the regular sync uses.
//
// The flag set is intentionally smaller than sync's — flags that
// only make sense for pushing (--prune, --dry-run, --strategy)
// don't appear here.
func newPullCmd() *cobra.Command {
	var (
		nsFilters   []string
		configPath  string
		wait        bool
		waitTimeout time.Duration
		pollEvery   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Download the current server state into the local translation files (no push)",
		Long: `Pulls every attached language's translations for each namespace in
.langsync.json and writes them to disk. Equivalent to
'norcube langsync sync --strategy server' — use whichever you find
more discoverable. Pull never modifies the server; if you want to
push local edits, use ` + "`norcube langsync sync`" + `.

By default sync waits for any in-flight server-side autotranslate to
finish before returning, so the on-disk files are complete. Pass
--wait=false to return immediately with whatever's there now.

Examples:
  norcube langsync pull
  norcube langsync pull -n ytracker-backend
  norcube langsync pull --wait=false`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			path, cfg, err := loadProjectConfig(configPath)
			if err != nil {
				return err
			}
			targets, err := selectSyncTargets(cfg, nsFilters)
			if err != nil {
				return err
			}
			opts := syncOptions{
				strategy:    strategyServer,
				wait:        wait,
				waitTimeout: waitTimeout,
				pollEvery:   pollEvery,
			}
			return runParallelSync(cmd, c, cfg, path, targets, opts)
		},
	}
	cmd.Flags().StringSliceVarP(&nsFilters, "namespace", "n", nil, "Pull only this namespace (repeat for multiple)")
	cmd.Flags().StringVar(&configPath, "config", "", "Path to .langsync.json (defaults to walking up from the cwd)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Block until any in-flight autotranslate has drained")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "Client-side timeout for the polling loop")
	cmd.Flags().DurationVar(&pollEvery, "poll-every", 1*time.Second, "How often to poll the job's state")
	return cmd
}

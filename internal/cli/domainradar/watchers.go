package domainradar

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newWatchersCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "watchers",
		Aliases: []string{"watcher", "brand-watchers"},
		Short:   "Manage brand watchers that look for lookalike domains",
		Long: `A brand watcher looks for your brand word in newly issued TLS
certificates and records lookalike domains as hits ("domainradar hits").
Each enabled watcher is billed per month; the first is free.`,
	}
	cmd.AddCommand(
		newWatcherListCmd(d),
		newWatcherGetCmd(d),
		newWatcherCreateCmd(d),
		newWatcherUpdateCmd(d),
		newWatcherDeleteCmd(d),
	)
	return cmd
}

var watcherHeaders = []string{"ID", "WORD", "STATUS", "MIN SCORE", "AI CHECK", "ALLOWLIST", "CREATED"}

func watcherRow(w domainradar.DtoWatcher) []string {
	status := "disabled"
	if cmdutil.Deref(w.Enabled) {
		status = "active"
	}
	return []string{
		cmdutil.Deref(w.Id),
		cmdutil.Deref(w.Word),
		status,
		fmt.Sprint(cmdutil.Deref(w.MinNotificationScore)),
		onOff(w.AiContentCheck),
		joinList(w.Allowlist),
		output.WhenString(cmdutil.Deref(w.CreatedAt)),
	}
}

func watcherObject(w domainradar.DtoWatcher) output.Object {
	row := watcherRow(w)
	fields := make([]output.Field, 0, len(row)+1)
	for i, h := range watcherHeaders {
		fields = append(fields, output.Field{Name: titleCase(h), Value: row[i]})
	}
	fields = append(fields, output.Field{Name: "Notify", Value: cmdutil.Deref(w.NotificationEmail)})
	return output.Object{Value: w, Fields: fields}
}

// titleCase turns a table header ("MIN SCORE") into a field name
// ("Min score").
func titleCase(h string) string {
	h = strings.ToLower(h)
	if h == "id" || h == "ai check" {
		return strings.ToUpper(h[:2]) + h[2:]
	}
	return strings.ToUpper(h[:1]) + h[1:]
}

func newWatcherListCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List brand watchers",
		Example: `  norcube domainradar watchers list`,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetBrandMonitorWatchersWithResponse(cmd.Context())
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Table[domainradar.DtoWatcher]{
				Headers:   watcherHeaders,
				MaxWidths: []int{0, 0, 0, 0, 0, 40, 0},
				Style:     output.Style{StatusColumn: 2},
				Rows:      watcherRow,
				Items:     *res.JSON200,
			}, nil
		}),
	}
}

func newWatcherGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:     "get <watcher-id>",
		Short:   "Show a brand watcher",
		Example: `  norcube domainradar watchers get 7d2e…`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetBrandMonitorWatchersIdWithResponse(cmd.Context(), args[0])
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return watcherObject(*res.JSON200), nil
		}),
	}
}

// watcherFlags are the settings create and update share.
type watcherFlags struct {
	word      string
	allow     []string
	minScore  int
	aiCheck   bool
	notify    string
	enabled   bool
	disabled  bool
	clearAllo bool
}

func addWatcherFlags(cmd *cobra.Command, f *watcherFlags, update bool) {
	cmd.Flags().StringSliceVar(&f.allow, "allow", nil, "Domains that are yours and never count as hits (repeat or comma-separate)")
	cmd.Flags().IntVar(&f.minScore, "min-score", 0, "Only email about hits scoring at least this (0-100)")
	cmd.Flags().BoolVar(&f.aiCheck, "ai-check", false, "Let AI review each hit's website for impersonation (billed per check)")
	cmd.Flags().StringVar(&f.notify, "notify", "", "Email address for hit alerts")
	if update {
		cmd.Flags().StringVar(&f.word, "word", "", "New brand word to watch")
		cmd.Flags().BoolVar(&f.enabled, "enable", false, "Turn the watcher on")
		cmd.Flags().BoolVar(&f.disabled, "disable", false, "Turn the watcher off (stops billing for it)")
		cmd.Flags().BoolVar(&f.clearAllo, "clear-allow", false, "Empty the allowlist")
		cmd.MarkFlagsMutuallyExclusive("enable", "disable")
		cmd.MarkFlagsMutuallyExclusive("allow", "clear-allow")
	}
}

func (f watcherFlags) validate() error {
	if f.minScore < 0 || f.minScore > 100 {
		return clierr.Usage(fmt.Errorf("--min-score must be between 0 and 100"))
	}
	return nil
}

func newWatcherCreateCmd(d deps) *cobra.Command {
	var f watcherFlags
	cmd := &cobra.Command{
		Use:   "create <word>",
		Short: "Start watching for lookalikes of a brand word",
		Long: `Creates an enabled brand watcher for a word (your brand, product or
trademark). Hits appear under "domainradar hits" as certificates for
lookalike domains are issued.

Each enabled watcher is billed per month (the first is free); --ai-check
adds a billed AI review per hit.`,
		Example: `  norcube domainradar watchers create acme --allow acme.com,acme.io --notify security@acme.com
  norcube domainradar watchers create acme --min-score 60 --ai-check`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if err := f.validate(); err != nil {
				return nil, err
			}
			word := strings.TrimSpace(args[0])
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			body := domainradar.PostBrandMonitorWatchersJSONRequestBody{Word: &word}
			if len(f.allow) > 0 {
				body.Allowlist = &f.allow
			}
			if cmd.Flags().Changed("min-score") {
				body.MinNotificationScore = &f.minScore
			}
			if cmd.Flags().Changed("ai-check") {
				body.AiContentCheck = &f.aiCheck
			}
			if f.notify != "" {
				body.NotificationEmail = &f.notify
			}
			res, err := c.API.PostBrandMonitorWatchersWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			w := *res.JSON201
			return output.Done{
				Message: fmt.Sprintf("Watching for lookalikes of %q (watcher %s).", word, cmdutil.Deref(w.Id)),
				Value:   w,
				Fields:  []output.Field{{Value: cmdutil.Deref(w.Id)}, {Value: word}},
			}, nil
		}),
	}
	addWatcherFlags(cmd, &f, false)
	return cmd
}

func newWatcherUpdateCmd(d deps) *cobra.Command {
	var f watcherFlags
	cmd := &cobra.Command{
		Use:   "update <watcher-id>",
		Short: "Change a brand watcher's settings",
		Long:  "Changes only the settings you pass. --disable stops the watcher and its billing; --enable turns it back on.",
		Example: `  norcube domainradar watchers update 7d2e… --disable
  norcube domainradar watchers update 7d2e… --min-score 70 --allow acme.com,acme.io`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if err := f.validate(); err != nil {
				return nil, err
			}
			body := domainradar.PutBrandMonitorWatchersIdJSONRequestBody{}
			changed := false
			set := func(name string, apply func()) {
				if cmd.Flags().Changed(name) {
					apply()
					changed = true
				}
			}
			set("word", func() { w := strings.TrimSpace(f.word); body.Word = &w })
			set("allow", func() { body.Allowlist = &f.allow })
			set("clear-allow", func() { body.Allowlist = &[]string{} })
			set("min-score", func() { body.MinNotificationScore = &f.minScore })
			set("ai-check", func() { body.AiContentCheck = &f.aiCheck })
			set("notify", func() { body.NotificationEmail = &f.notify })
			set("enable", func() { t := true; body.Enabled = &t })
			set("disable", func() { v := false; body.Enabled = &v })
			if !changed {
				return nil, errNotSet("--word, --allow, --clear-allow, --min-score, --ai-check, --notify, --enable, --disable")
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.PutBrandMonitorWatchersIdWithResponse(cmd.Context(), args[0], body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			w := *res.JSON200
			return output.Done{
				Message: fmt.Sprintf("Updated watcher %s (%q).", args[0], cmdutil.Deref(w.Word)),
				Value:   w,
				Fields:  []output.Field{{Value: cmdutil.Deref(w.Id)}, {Value: cmdutil.Deref(w.Word)}},
			}, nil
		}),
	}
	addWatcherFlags(cmd, &f, true)
	return cmd
}

func newWatcherDeleteCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <watcher-id>",
		Short: "Delete a brand watcher and its hits",
		Long:  "Deletes the watcher and every hit it recorded. To pause it instead, use \"watchers update --disable\".",
		Example: `  norcube domainradar watchers delete 7d2e…
  norcube domainradar watchers delete 7d2e… --yes`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Delete watcher %s and all its hits? This can't be undone", args[0]), yes); err != nil || !ok {
				return nil, cancelled(cmd, err)
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.DeleteBrandMonitorWatchersIdWithResponse(cmd.Context(), args[0])
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted watcher %s.", args[0]),
				Value:   map[string]any{"id": args[0], "deleted": true},
				Fields:  []output.Field{{Value: args[0]}},
			}, nil
		}),
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Don't ask for confirmation")
	return cmd
}

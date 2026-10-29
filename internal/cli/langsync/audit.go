package langsync

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// auditActions and auditTargetTypes are offered as completions; the
// filters pass any value through, so new server-side actions work too.
var (
	auditActions = []string{
		"namespace.created", "namespace.updated", "namespace.deleted",
		"namespace.language_added", "namespace.language_removed",
		"apikey.created", "apikey.deleted",
		"glossary.created", "glossary.language_attached",
		"glossary_item.created", "glossary_item.updated", "glossary_item.deleted",
		"custom_language.created", "custom_language.deleted",
		"custom_prompt.created", "custom_prompt.updated", "custom_prompt.deleted",
		"sync_job.created", "sync_job.completed", "sync_job.failed",
		"term_import.finished", "migrator.imported",
	}
	auditTargetTypes = []string{
		"namespace", "apikey", "glossary", "glossary_item", "custom_language",
		"custom_prompt", "sync_job", "term_import", "migrator",
	}
)

// auditCursorSep joins the two parts of the audit log's cursor (a
// timestamp and an entry id) into the single --cursor value.
const auditCursorSep = "_"

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Read the organization's LangSync audit log",
	}
	cmd.AddCommand(newAuditListCmd())
	return cmd
}

func newAuditListCmd() *cobra.Command {
	var (
		since, until, target         string
		actions, targetTypes, actors []string
		pages                        cmdutil.PageFlags
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List audit log entries, newest first",
		Long: `Lists who changed what in LangSync: namespaces, API keys, the glossary,
custom languages and prompts, sync jobs and imports. Reads are not
recorded.

--since and --until take an RFC 3339 timestamp, a date (2026-10-28) or
an age (24h, 7d). Repeat --action, --target-type and --actor (or
comma-separate them) to match any of several values. With -o json each
entry includes its metadata, IP address and user agent.`,
		Example: `  norcube langsync audit list --since 7d
  norcube langsync audit list --action apikey.created,apikey.deleted
  norcube langsync audit list --target-type namespace --all-pages -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			params := langsync.ListAuditLogsParams{}
			now := time.Now()
			if since != "" {
				t, err := cmdutil.ParseTimeArg("since", since, now)
				if err != nil {
					return err
				}
				s := t.Format(time.RFC3339)
				params.Since = &s
			}
			if until != "" {
				t, err := cmdutil.ParseTimeArg("until", until, now)
				if err != nil {
					return err
				}
				u := t.Format(time.RFC3339)
				params.Until = &u
			}
			if len(actions) > 0 {
				params.Actions = &actions
			}
			if len(targetTypes) > 0 {
				params.TargetTypes = &targetTypes
			}
			if len(actors) > 0 {
				params.ActorIds = &actors
			}
			if target != "" {
				params.TargetId = &target
			}
			if pages.PageSize > 200 {
				return clierr.Usage(errors.New("--page-size must be at most 200 for the audit log"))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]langsync.AuditPayloadAuditLogEntry, string, error) {
				p := params
				p.Limit = &size
				if cursor != "" {
					at, id, ok := strings.Cut(cursor, auditCursorSep)
					if !ok || at == "" || id == "" {
						return nil, "", clierr.Usage(errors.New("--cursor is not an audit log cursor (copy it from the hint a previous page printed)"))
					}
					p.CursorCreatedAt, p.CursorId = &at, &id
				}
				res, err := c.API.ListAuditLogsWithResponse(ctx, &p)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
				}
				next := ""
				if at, id := cmdutil.Deref(res.JSON200.NextCursorCreatedAt), cmdutil.Deref(res.JSON200.NextCursorId); at != "" && id != "" {
					next = at + auditCursorSep + id
				}
				return cmdutil.Deref(res.JSON200.List), next, nil
			})
			if err != nil {
				return err
			}
			return page.Print(cmd, output.Table[langsync.AuditPayloadAuditLogEntry]{
				Headers:   []string{"TIME", "ACTOR", "ACTION", "TARGET", "TARGET ID"},
				MaxWidths: []int{0, 32, 0, 0, 0},
				Rows: func(e langsync.AuditPayloadAuditLogEntry) []string {
					return []string{
						compactDate(cmdutil.Deref(e.CreatedAt)), auditActor(e),
						string(cmdutil.Deref(e.Action)), string(cmdutil.Deref(e.TargetType)), cmdutil.Deref(e.TargetId),
					}
				},
				Items: page.Items,
			})
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Only entries at or after this time (timestamp, date or age such as 7d)")
	cmd.Flags().StringVar(&until, "until", "", "Only entries before this time (timestamp, date or age)")
	cmd.Flags().StringSliceVar(&actions, "action", nil, "Only these actions, e.g. namespace.updated (repeat or comma-separate)")
	cmd.Flags().StringSliceVar(&targetTypes, "target-type", nil, "Only these target types, e.g. namespace, apikey (repeat or comma-separate)")
	cmd.Flags().StringVar(&target, "target", "", "Only entries about this target id")
	cmd.Flags().StringSliceVar(&actors, "actor", nil, "Only entries by these user ids (repeat or comma-separate)")
	_ = cmd.RegisterFlagCompletionFunc("action", cobra.FixedCompletions(auditActions, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("target-type", cobra.FixedCompletions(auditTargetTypes, cobra.ShellCompDirectiveNoFileComp))
	cmdutil.AddPageFlags(cmd, &pages, defaultMarkPageLimit, maxMarkItems)
	return cmd
}

// auditActor names who acted: the person, or the kind of actor (an API
// key, the system) when there is no person.
func auditActor(e langsync.AuditPayloadAuditLogEntry) string {
	name, email := cmdutil.Deref(e.ActorName), cmdutil.Deref(e.ActorEmail)
	switch {
	case name != "" && email != "":
		return name + " <" + email + ">"
	case name != "":
		return name
	case email != "":
		return email
	case cmdutil.Deref(e.ActorType) != "":
		return cmdutil.Deref(e.ActorType)
	}
	return cmdutil.Deref(e.ActorId)
}

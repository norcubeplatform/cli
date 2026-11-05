package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newStatusPageSubscribersCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "subscribers",
		Aliases: []string{"subscriber"},
		Short:   "See and delete the people who get a status page's email updates",
		Long: `Subscribers are people who asked for email updates through the form on a
status page. An address stays pending until it confirms from the mail it
gets, and is removed if it hasn't after 7 days; only a confirmed address
gets updates. Every mail sent counts as an email notification in your
Norcube Monitor usage.

Updates reach subscribers when you post with --notify ("incidents create",
"incidents updates create") and when the page's --auto-post posts for you.

Addresses are personal data. Listing or deleting them takes the
monitor:ManageStatusPageSubscribers permission and, on a CI token, the
monitor.statuspages scope: no other scope reaches them, not even one that
reads the page itself.`,
	}
	cmd.AddCommand(newSubscriberListCmd(d), newSubscriberDeleteCmd(d))
	return cmd
}

func newSubscriberListCmd(d deps) *cobra.Command {
	var pf cmdutil.PageFlags
	cmd := &cobra.Command{
		Use:         "list <page>",
		Annotations: cmdutil.Reads(),
		Short:       "List a status page's subscribers, newest first",
		Long: `Lists confirmed and pending subscribers. COMPONENTS is "all", or how many
of the page's components the subscriber follows; -o json has their ids.

The output holds personal data, so keep it out of build logs and other places
many people can read.`,
		Example: `  norcube monitor status-pages subscribers list acme
  norcube monitor status-pages subscribers list acme --all-pages -o json --query "[?status=='confirmed'].email"`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]monitor.DtoStatusPageSubscriber, string, error) {
				p := monitor.ListStatusPageSubscribersParams{Limit: &size}
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListStatusPageSubscribersWithResponse(ctx, id, &p)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", subscriberError(res.HTTPResponse, res.Body)
				}
				return res.JSON200.List, nextCursor(res.JSON200.Cursors), nil
			})
			if err != nil {
				return nil, err
			}
			return nil, page.Print(cmd, subscriberTable(page.Items))
		}),
	}
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

func newSubscriberDeleteCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:         "delete <page> <subscriber>",
		Annotations: cmdutil.Writes(),
		Short:       "Delete a subscriber and any mail queued for them",
		Long: `Deletes the subscriber by id ("subscribers list" shows it) and any mail
queued for them. Their address isn't kept, so they can subscribe again.`,
		Example: `  norcube monitor status-pages subscribers delete acme 3f2a1b0c-9d8e-4f7a-b6c5-d4e3f2a1b0c9
  norcube monitor status-pages subscribers delete acme 3f2a1b0c-9d8e-4f7a-b6c5-d4e3f2a1b0c9 --yes`,
		Args: cobra.ExactArgs(2),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			subscriberID, err := idArg("subscriber", args[1])
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Delete subscriber %s from status page %s, with any mail queued for them?", subscriberID, args[0]), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			res, err := c.API.DeleteStatusPageSubscriberWithResponse(cmd.Context(), id, subscriberID)
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent {
				return nil, subscriberError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted subscriber %s from status page %s.", subscriberID, args[0]),
				Value:   deleted{ID: subscriberID.String(), Deleted: true},
				Fields:  []output.Field{{Value: subscriberID.String()}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask for confirmation")
	return cmd
}

// subscriberError renders a refused subscriber request. The API's scope
// refusal is shared with the page's other writes and speaks of changing pages,
// which misleads on a list, so this one names what was refused. The code and
// exit code stay those of any other scope refusal.
func subscriberError(resp *http.Response, body []byte) error {
	err := apiError(resp, body)
	var e *clierr.Error
	if !errors.As(err, &e) {
		return err
	}
	switch e.Code {
	case clierr.CodeInsufficientScope:
		e.Message = "This token can't list or delete a status page's subscribers: their addresses are personal data, and it needs the monitor.statuspages scope."
	case clierr.CodePermissionDenied:
		e.Hint = "Addresses are personal data: this takes the monitor:ManageStatusPageSubscribers permission in your organization."
	}
	return err
}

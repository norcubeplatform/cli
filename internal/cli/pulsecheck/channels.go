package pulsecheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/pulsecheck"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/term"
)

// channelKinds are the kinds a channel can be created with (voice-call
// channels made before calls were withdrawn can only be renamed or
// deleted).
var channelKinds = []string{"email", "webhook", "slack", "discord", "sms"}

var deliveryStatuses = []string{"pending", "sent", "dead"}

func newChannelsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "channels",
		Aliases: []string{"channel", "alert-channels"},
		Short:   "Manage alert channels: email, webhooks, Slack, Discord and SMS",
		Long: `Alert channels are where incidents alert. Default channels get the
alerts of every monitor that follows the organization's defaults; a
monitor's own routes ("monitors alerts set") name channels directly.

Credentials stay secret: Slack and Discord URLs are read from a file or
standard input and shown only as a hint, and a webhook's signing secret is
written to a file you name, never printed. Email-address and SMS channels
get no alerts until their contact confirms a code ("channels verify").
Changing channels needs a signed-in person; CI tokens can only read them.`,
	}
	cmd.AddCommand(
		newChannelListCmd(d), newChannelGetCmd(d), newChannelCreateCmd(d), newChannelUpdateCmd(d),
		newChannelDeleteCmd(d), newChannelTestCmd(d), newChannelVerifyCmd(d), newDeliveriesCmd(d),
	)
	return cmd
}

func newChannelListCmd(d deps) *cobra.Command {
	var pf cmdutil.PageFlags
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: cmdutil.Reads(),
		Short:       "List alert channels",
		Example:     `  norcube pulsecheck channels list`,
		Args:        cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]pulsecheck.DtoAlertChannel, string, error) {
				p := pulsecheck.ListAlertChannelsParams{Limit: &size}
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListAlertChannelsWithResponse(ctx, &p)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", apiError(res.HTTPResponse, res.Body)
				}
				return res.JSON200.List, nextCursor(res.JSON200.Cursors), nil
			})
			if err != nil {
				return nil, err
			}
			return nil, page.Print(cmd, channelTable(page.Items))
		}),
	}
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

func newChannelGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "get <channel>",
		Annotations: cmdutil.Reads(),
		Short:       "Show an alert channel",
		Example:     `  norcube pulsecheck channels get "On-call webhook"`,
		Args:        cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			ch, err := getChannel(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			return channelObject(ch), nil
		}),
	}
}

func getChannel(ctx context.Context, c *pcContext, ref string) (pulsecheck.DtoAlertChannel, error) {
	id, err := channelRef(ctx, c, ref)
	if err != nil {
		return pulsecheck.DtoAlertChannel{}, err
	}
	res, err := c.API.GetAlertChannelWithResponse(ctx, id)
	if err != nil {
		return pulsecheck.DtoAlertChannel{}, err
	}
	if res.JSON200 == nil {
		return pulsecheck.DtoAlertChannel{}, apiError(res.HTTPResponse, res.Body)
	}
	return *res.JSON200, nil
}

// channelFlags are the settings create and update share.
type channelFlags struct {
	address, url, urlFile, phone string
	signingSecretFile, secretOut string
	members, isDefault           bool
}

func addChannelFlags(cmd *cobra.Command, f *channelFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.address, "address", "", "email: the address to alert (it gets a code to confirm)")
	fl.BoolVar(&f.members, "members", false, "email: alert the organization's members instead of one address")
	fl.StringVar(&f.url, "url", "", "webhook: the URL PulseCheck posts signed alerts to")
	fl.StringVar(&f.urlFile, "url-file", "", "slack, discord: read the incoming-webhook URL (a credential) from this file (- for standard input)")
	fl.StringVar(&f.phone, "phone", "", "sms: a mobile number in international format, e.g. +420601123456 (it gets a code to confirm)")
	fl.StringVar(&f.signingSecretFile, "signing-secret-file", "", "webhook: use your own signing secret (whsec_…) read from this file (- for standard input)")
	fl.StringVar(&f.secretOut, "secret-file", "", "webhook: write the generated signing secret to this new file (mode 0600); it is never printed")
	fl.BoolVar(&f.isDefault, "default", false, "Make it a default channel: monitors that follow the organization's defaults alert here")
	cmd.MarkFlagsMutuallyExclusive("address", "members")
	cmd.MarkFlagsMutuallyExclusive("signing-secret-file", "secret-file")
}

// settingsFor builds a kind's settings from the flags, refusing flags of
// other kinds and credentials in arguments.
func settingsFor(cmd *cobra.Command, kind string, f channelFlags) (*pulsecheck.DtoAlertChannelSettingsInput, error) {
	set := cmd.Flags().Changed
	allowed := map[string][]string{
		"email":   {"address", "members"},
		"webhook": {"url", "signing-secret-file", "secret-file"},
		"slack":   {"url-file"},
		"discord": {"url-file"},
		"sms":     {"phone"},
	}[kind]
	for _, n := range []string{"address", "members", "url", "url-file", "phone", "signing-secret-file", "secret-file"} {
		if set(n) && !strings.Contains(" "+strings.Join(allowed, " ")+" ", " "+n+" ") {
			if n == "url" && (kind == "slack" || kind == "discord") {
				return nil, clierr.Usage(fmt.Errorf("a %s webhook URL is a credential: pass it with --url-file (- reads standard input), not as an argument", kind))
			}
			return nil, clierr.Usage(fmt.Errorf("--%s doesn't apply to %s channels", n, kind))
		}
	}
	in := &pulsecheck.DtoAlertChannelSettingsInput{}
	any := false
	switch kind {
	case "email":
		if f.address != "" {
			in.Address, any = &f.address, true
		}
		if f.members {
			in.Members, any = &f.members, true
		}
	case "webhook":
		if f.url != "" {
			in.Url, any = &f.url, true
		}
		if f.signingSecretFile != "" {
			s, err := cmdutil.ReadText(cmd.InOrStdin(), "", f.signingSecretFile, "the signing secret")
			if err != nil {
				return nil, err
			}
			s = strings.TrimSpace(s)
			in.SigningSecret, any = &s, true
		}
	case "slack", "discord":
		if f.urlFile != "" {
			u, err := cmdutil.ReadText(cmd.InOrStdin(), "", f.urlFile, "the webhook URL")
			if err != nil {
				return nil, err
			}
			u = strings.TrimSpace(u)
			in.Url, any = &u, true
		}
	case "sms":
		if f.phone != "" {
			in.Phone, any = &f.phone, true
		}
	}
	if !any {
		return nil, nil
	}
	return in, nil
}

func newChannelCreateCmd(d deps) *cobra.Command {
	var (
		f    channelFlags
		kind string
	)
	cmd := &cobra.Command{
		Use:         "create <name>",
		Annotations: cmdutil.Writes(),
		Short:       "Create an alert channel",
		Long: `Creates a channel of one kind:

  email    --address (confirmed with a code sent there) or --members
  webhook  --url; signed with a secret PulseCheck generates (saved with
           --secret-file, never printed) or yours (--signing-secret-file)
  slack    --url-file: the incoming-webhook URL, from a file or stdin
  discord  --url-file: the webhook URL, from a file or stdin
  sms      --phone (confirmed with a code texted there)

Email-address and SMS channels get no alerts until verified: run
"channels verify <channel>" (on a terminal it asks for the code).`,
		Example: `  norcube pulsecheck channels create "Ops" --kind email --address ops@example.com --default
  norcube pulsecheck channels create "On-call webhook" --kind webhook --url https://example.com/hooks/pulsecheck --secret-file webhook-secret.txt
  pbpaste | norcube pulsecheck channels create "#alerts" --kind slack --url-file -
  norcube pulsecheck channels create "On-call phone" --kind sms --phone +420601123456`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if kind == "" {
				return nil, clierr.Usage(fmt.Errorf("--kind is required: one of %s", strings.Join(channelKinds, ", ")))
			}
			if err := oneOf("--kind", kind, channelKinds); err != nil {
				return nil, err
			}
			if f.secretOut == "-" {
				return nil, clierr.Usage(fmt.Errorf("--secret-file needs a file name: the signing secret is never printed"))
			}
			settings, err := settingsFor(cmd, kind, f)
			if err != nil {
				return nil, err
			}
			if settings == nil {
				need := map[string]string{"email": "--address or --members", "webhook": "--url", "slack": "--url-file", "discord": "--url-file", "sms": "--phone"}[kind]
				return nil, clierr.Usage(fmt.Errorf("%s channels need %s", kind, need))
			}
			body := pulsecheck.DtoAlertChannelCreate{Kind: pulsecheck.DtoAlertChannelCreateKind(kind), Name: strings.TrimSpace(args[0]), Settings: *settings}
			if cmd.Flags().Changed("default") {
				body.IsDefault = &f.isDefault
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.CreateAlertChannelWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			created := *res.JSON201
			if err := keepSecret(cmd, created, f.secretOut); err != nil {
				return nil, err
			}
			ch := withoutSecret(created)
			msg := fmt.Sprintf("Created %s channel %s (%s).", ch.Kind, ch.Name, ch.Id)
			if !ch.Verified && (kind == "sms" || (kind == "email" && !f.members)) {
				msg += " Confirm it with: norcube pulsecheck channels verify " + ch.Id.String()
			}
			return output.Done{Message: msg, Value: ch, Fields: []output.Field{{Value: ch.Id.String()}, {Value: ch.Name}}}, nil
		}),
	}
	cmd.Flags().StringVar(&kind, "kind", "", "Channel kind: email, webhook, slack, discord or sms")
	cmdutil.FlagEnum(cmd, "kind", channelKinds...)
	addChannelFlags(cmd, &f)
	return cmd
}

// keepSecret writes a signing secret the API returned this once to the
// file named by --secret-file, or says on stderr that it was not kept.
// It never reaches stdout.
func keepSecret(cmd *cobra.Command, ch pulsecheck.DtoAlertChannelWithSecret, dest string) error {
	secret := cmdutil.Deref(ch.SigningSecret)
	if secret == "" {
		return nil
	}
	if dest == "" {
		fmt.Fprintf(cmdutil.Stderr(cmd), "The webhook's signing secret was not kept (pass --secret-file to save it). Get a new one with: norcube pulsecheck channels update %s --rotate-secret --secret-file <file>\n", ch.Id)
		return nil
	}
	if _, err := cmdutil.SaveStream(cmd, strings.NewReader(secret+"\n"), dest, 0o600); err != nil {
		return fmt.Errorf("the channel was saved, but its signing secret could not be written to %s: %w (rotate it with --rotate-secret --secret-file)", dest, err)
	}
	fmt.Fprintf(cmdutil.Stderr(cmd), "Wrote the webhook's signing secret to %s.\n", dest)
	return nil
}

func newChannelUpdateCmd(d deps) *cobra.Command {
	var (
		f               channelFlags
		name            string
		enable, disable bool
		rotate          bool
	)
	cmd := &cobra.Command{
		Use:         "update <channel>",
		Annotations: cmdutil.Writes(),
		Short:       "Rename, enable, disable or reconfigure an alert channel",
		Long: `Changes only what you pass. New settings replace the kind's settings as a
whole (a Slack or Discord URL must be given again); a new address or phone
number needs verifying again. --rotate-secret gives a webhook a new
signing secret, written to --secret-file.`,
		Example: `  norcube pulsecheck channels update "Ops" --default=false
  norcube pulsecheck channels update "On-call webhook" --rotate-secret --secret-file new-secret.txt
  norcube pulsecheck channels update "On-call phone" --phone +420601999888`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if f.secretOut == "-" {
				return nil, clierr.Usage(fmt.Errorf("--secret-file needs a file name: the signing secret is never printed"))
			}
			if rotate && f.signingSecretFile != "" {
				return nil, clierr.Usage(fmt.Errorf("--rotate-secret and --signing-secret-file can't be combined"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			ch, err := getChannel(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			kind := string(ch.Kind)
			if rotate && kind != "webhook" {
				return nil, clierr.Usage(fmt.Errorf("--rotate-secret is for webhook channels"))
			}
			settings, err := settingsFor(cmd, kind, f)
			if err != nil {
				return nil, err
			}
			body := pulsecheck.DtoAlertChannelUpdate{Settings: settings}
			changed := settings != nil
			if cmd.Flags().Changed("name") {
				n := strings.TrimSpace(name)
				body.Name, changed = &n, true
			}
			if cmd.Flags().Changed("default") {
				body.IsDefault, changed = &f.isDefault, true
			}
			if enable || disable {
				v := enable
				body.Enabled, changed = &v, true
			}
			if rotate {
				body.RotateSecret, changed = &rotate, true
			}
			if !changed {
				return nil, clierr.Usage(fmt.Errorf("nothing to change: pass --name, --default, --enable, --disable, --rotate-secret or new settings"))
			}
			res, err := c.API.UpdateAlertChannelWithResponse(cmd.Context(), ch.Id, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			if err := keepSecret(cmd, *res.JSON200, f.secretOut); err != nil {
				return nil, err
			}
			u := withoutSecret(*res.JSON200)
			return output.Done{
				Message: fmt.Sprintf("Updated channel %s (%s).", u.Name, u.Id),
				Value:   u,
				Fields:  []output.Field{{Value: u.Id.String()}, {Value: u.Name}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&name, "name", "", "New name")
	cmd.Flags().BoolVar(&enable, "enable", false, "Send alerts through the channel again")
	cmd.Flags().BoolVar(&disable, "disable", false, "Stop sending alerts through the channel, keeping it")
	cmd.Flags().BoolVar(&rotate, "rotate-secret", false, "webhook: generate a new signing secret (use with --secret-file)")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	addChannelFlags(cmd, &f)
	return cmd
}

func newChannelDeleteCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:         "delete <channel>",
		Annotations: cmdutil.Writes(),
		Short:       "Delete an alert channel",
		Long:        "Deletes the channel and removes it from every monitor's alert routes; alerts still queued for it are dropped.",
		Example: `  norcube pulsecheck channels delete "Old webhook"
  norcube pulsecheck channels delete 0c2d4f6a-8b1e-4c3d-9e5f-7a6b5c4d3e2f --yes`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := channelRef(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Delete alert channel %s? Monitors routed to it stop alerting there", args[0]), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			res, err := c.API.DeleteAlertChannelWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted channel %s.", args[0]),
				Value:   deleted{ID: id.String(), Deleted: true},
				Fields:  []output.Field{{Value: id.String()}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask for confirmation")
	return cmd
}

func newChannelTestCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:         "test <channel>",
		Annotations: cmdutil.Writes(),
		Short:       "Send a test alert through a channel now",
		Long: `Sends a test alert right away and prints the provider's reference. Rate
limited (20 per user and 60 per organization an hour); an SMS counts
toward the daily SMS limit, so SMS channels ask first (--yes skips it).`,
		Example: `  norcube pulsecheck channels test "On-call webhook"`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			ch, err := getChannel(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if ch.Kind == "sms" {
				if ok, err := cmdutil.Confirm(fmt.Sprintf("Text a test alert to %s? It counts toward the daily SMS limit", destination(ch)), yes); err != nil || !ok {
					return nil, cmdutil.Cancelled(cmd, err)
				}
			}
			res, err := c.API.TestAlertChannelWithResponse(cmd.Context(), ch.Id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Sent a test alert through %s (provider reference %s).", ch.Name, res.JSON200.ProviderRef),
				Value:   *res.JSON200,
				Fields:  []output.Field{{Value: res.JSON200.ProviderRef}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask before texting an SMS channel")
	return cmd
}

var verificationCode = regexp.MustCompile(`^[0-9]{6}$`)

// verifyResult is what `channels verify` prints as JSON.
type verifyResult struct {
	ID       string `json:"id" yaml:"id"`
	CodeSent bool   `json:"codeSent" yaml:"codeSent"`
	Verified bool   `json:"verified" yaml:"verified"`
}

func newChannelVerifyCmd(d deps) *cobra.Command {
	var (
		codeFile string
		resend   bool
	)
	cmd := &cobra.Command{
		Use:         "verify <channel>",
		Annotations: cmdutil.Writes(),
		Short:       "Confirm an SMS or email-address channel with the code sent to it",
		Long: `Email-address and SMS channels get no alerts until their contact
confirms a 6-digit code (valid 10 minutes, 5 attempts).

On a terminal, verify sends a code and asks for it. In a script, run it
once to send the code, then again with --code-file (- reads standard
input) to confirm. The code is never taken as an argument nor printed.
A new code can be sent after a minute (--resend), at most 5 a day.`,
		Example: `  norcube pulsecheck channels verify "On-call phone"
  norcube pulsecheck channels verify 0c2d4f6a-8b1e-4c3d-9e5f-7a6b5c4d3e2f --resend
  echo 123456 | norcube pulsecheck channels verify 0c2d4f6a-8b1e-4c3d-9e5f-7a6b5c4d3e2f --code-file -`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if resend && codeFile != "" {
				return nil, clierr.Usage(fmt.Errorf("--resend and --code-file can't be combined"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := channelRef(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			stderr := cmdutil.Stderr(cmd)
			var code string
			sent := false
			if codeFile == "" {
				if err := sendCode(cmd.Context(), c, id); err != nil {
					return nil, err
				}
				sent = true
				if !term.StdinIsTerminal() {
					fmt.Fprintf(stderr, "Sent a code. Confirm it with: norcube pulsecheck channels verify %s --code-file -\n", id)
					return output.Done{Message: "Code sent.", Value: verifyResult{ID: id.String(), CodeSent: true},
						Fields: []output.Field{{Value: id.String()}, {Value: "sent"}}}, nil
				}
				code, err = askCode()
				if err != nil {
					if errors.Is(err, cmdutil.ErrCancelled) {
						return nil, cmdutil.Cancelled(cmd, nil)
					}
					return nil, err
				}
			} else {
				code, err = cmdutil.ReadText(cmd.InOrStdin(), "", codeFile, "the code")
				if err != nil {
					return nil, err
				}
			}
			code = strings.TrimSpace(code)
			if !verificationCode.MatchString(code) {
				return nil, clierr.Usage(fmt.Errorf("the code is the 6 digits sent to the channel"))
			}
			res, err := c.API.ConfirmAlertChannelVerificationWithResponse(cmd.Context(), id, pulsecheck.DtoAlertChannelVerifyConfirm{Code: code})
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			ch := *res.JSON200
			return output.Done{
				Message: fmt.Sprintf("Verified channel %s (%s): it gets alerts now.", ch.Name, ch.Id),
				Value:   verifyResult{ID: ch.Id.String(), CodeSent: sent, Verified: ch.Verified},
				Fields:  []output.Field{{Value: ch.Id.String()}, {Value: "verified"}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&codeFile, "code-file", "", "Confirm with the code read from this file (- for standard input) instead of sending one")
	cmd.Flags().BoolVar(&resend, "resend", false, "Send a new code (the same as running verify again)")
	return cmd
}

func sendCode(ctx context.Context, c *pcContext, id uuid.UUID) error {
	res, err := c.API.SendAlertChannelVerificationWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if res.StatusCode() != http.StatusNoContent {
		return apiError(res.HTTPResponse, res.Body)
	}
	return nil
}

// askCode reads the code on a terminal without echoing it.
func askCode() (string, error) {
	var code string
	err := huh.NewInput().
		Title("Code").
		Description("The 6-digit code sent to the channel (valid 10 minutes).").
		EchoMode(huh.EchoModePassword).
		Validate(func(s string) error {
			if !verificationCode.MatchString(strings.TrimSpace(s)) {
				return fmt.Errorf("enter the 6 digits")
			}
			return nil
		}).
		Value(&code).
		Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return "", cmdutil.ErrCancelled
	}
	return code, err
}

// ---------- deliveries ----------

func newDeliveriesCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deliveries",
		Short: "The alerts a channel was given to send",
	}
	var (
		pf     cmdutil.PageFlags
		status string
	)
	list := &cobra.Command{
		Use:         "list <channel>",
		Annotations: cmdutil.Reads(),
		Short:       "List a channel's deliveries, newest first",
		Long: `Lists every alert given to the channel (kept 90 days): the event, monitor
and incident, whether it was sent, is pending (queued or waiting to retry)
or dead (given up), the attempts and the last error, with URLs, addresses
and phone numbers removed. --status dead lists the failures nobody saw.`,
		Example: `  norcube pulsecheck channels deliveries list "On-call webhook" --status dead`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			params := pulsecheck.ListAlertChannelDeliveriesParams{}
			if status != "" {
				if err := oneOf("--status", status, deliveryStatuses); err != nil {
					return nil, err
				}
				s := pulsecheck.ListAlertChannelDeliveriesParamsStatus(status)
				params.Status = &s
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := channelRef(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]pulsecheck.DtoChannelDelivery, string, error) {
				p := params
				p.Limit = &size
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListAlertChannelDeliveriesWithResponse(ctx, id, &p)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", apiError(res.HTTPResponse, res.Body)
				}
				return res.JSON200.List, nextCursor(res.JSON200.Cursors), nil
			})
			if err != nil {
				return nil, err
			}
			return nil, page.Print(cmd, output.Table[pulsecheck.DtoChannelDelivery]{
				Headers:   []string{"CREATED", "EVENT", "MONITOR", "STATUS", "ATTEMPTS", "SENT", "LAST ERROR"},
				MaxWidths: []int{0, 0, 28, 0, 0, 0, 60},
				Style:     output.Style{StatusColumn: 3},
				Rows: func(x pulsecheck.DtoChannelDelivery) []string {
					return []string{output.WhenString(x.CreatedAt), string(x.Event), x.MonitorName, string(x.Status),
						fmt.Sprint(x.Attempts), output.WhenString(cmdutil.Deref(x.SentAt)), strOr(x.LastError)}
				},
				Items: page.Items,
			})
		}),
	}
	list.Flags().StringVar(&status, "status", "", "Only pending, sent or dead deliveries")
	cmdutil.FlagEnum(list, "status", deliveryStatuses...)
	cmdutil.AddPageFlags(list, &pf, defaultPageSize, maxItems)
	cmd.AddCommand(list)
	return cmd
}

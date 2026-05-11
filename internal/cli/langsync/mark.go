package langsync

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
)

func newMarkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mark",
		Aliases: []string{"marks", "term", "terms"},
		Short:   "Manage marks (source strings) inside a namespace",
		Long: `In Langsync, a "mark" is the source string that other translations
target — stored on the term row as the `+"`mark`"+` column. Each mark
belongs to exactly one namespace within your active organization.`,
	}
	cmd.AddCommand(newMarkAddCmd())
	return cmd
}

func newMarkAddCmd() *cobra.Command {
	var (
		namespaceName  string
		context        string
		defaultValue   string
		autoTranslate  bool
	)
	cmd := &cobra.Command{
		Use:   `add "<source string>"`,
		Short: "Add a new mark to a namespace",
		Long: `Creates a new mark in the named namespace and, optionally, the value
for the namespace's default language. With --auto-translate, the backend
will try to fill in every other configured language using your AI
translation provider.

Examples:
  norcube langsync mark add --namespace web "Save changes"
  norcube langsync mark add --namespace web "Email" --context "form label" --default-value "E-mail"
  norcube langsync mark add --namespace web "Welcome" --default-value "Welcome" --auto-translate

If --namespace is omitted and stdin is a terminal, an interactive picker
lists every namespace in the active organization.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			mark := args[0]
			if mark == "" {
				return fmt.Errorf("the mark (source string) must not be empty")
			}

			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Add the new mark to which namespace?")
			if err != nil {
				if err == ErrCancelled {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			res, err := c.client.AddTermWithResponse(cmd.Context(), ns, langsync.AddTermJSONRequestBody{
				Mark:                   mark,
				Context:                context,
				DefaultLanguageValue:   defaultValue,
				TranslateAutomatically: autoTranslate,
			})
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Added mark %q to namespace %q.\n", mark, ns)
			if autoTranslate {
				fmt.Fprintln(out, "Auto-translation requested; check `norcube langsync mark list` (coming soon) to see progress.")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name (the URL slug); picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringVar(&context, "context", "", "Free-form context note for translators (e.g. \"form label\", \"button on signup page\")")
	cmd.Flags().StringVar(&defaultValue, "default-value", "", "Value for the namespace's default language (often identical to the mark itself)")
	cmd.Flags().BoolVar(&autoTranslate, "auto-translate", false, "Ask the backend to AI-translate the mark into every other configured language")
	return cmd
}

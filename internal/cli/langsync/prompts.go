package langsync

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newPromptsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "prompts",
		Aliases: []string{"prompt", "custom-prompts"},
		Short:   "Manage custom prompts for AI translation",
		Long: `A custom prompt is extra instructions for AI translation, such as tone,
audience or wording rules. A namespace can use one by default
(` + "`namespaces update <name> --default-prompt <id>`" + `), and a single run can name
another with --prompt.

Prompts are shared with the organization unless private; a private
prompt is visible to and usable by its author only.`,
	}
	cmd.AddCommand(newPromptListCmd(), newPromptGetCmd(), newPromptCreateCmd(), newPromptUpdateCmd(), newPromptDeleteCmd())
	return cmd
}

func listPrompts(cmd *cobra.Command, c *langsyncContext) ([]langsync.DtoCustomPrompt, error) {
	res, err := c.API.ListCustomPromptsWithResponse(cmd.Context())
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, apiError(res.HTTPResponse, res.Body, res.JSON401, res.JSON500)
	}
	return *res.JSON200, nil
}

func promptVisibility(p langsync.DtoCustomPrompt) string {
	if p.IsPrivate {
		return "private"
	}
	return "shared"
}

// oneLine shortens multi-line text to its first line for a table cell.
func oneLine(s string) string {
	first, _, more := strings.Cut(strings.TrimSpace(s), "\n")
	if more {
		return strings.TrimSpace(first) + " …"
	}
	return first
}

func newPromptListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the custom prompts you can use",
		Example: `  norcube langsync prompts list
  norcube langsync prompts list -o json --query "[?!isPrivate].id"`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			prompts, err := listPrompts(cmd, c)
			if err != nil {
				return nil, err
			}
			return output.Table[langsync.DtoCustomPrompt]{
				Headers:   []string{"ID", "NAME", "VISIBILITY", "UPDATED", "PROMPT"},
				MaxWidths: []int{0, 32, 0, 0, 48},
				Rows: func(p langsync.DtoCustomPrompt) []string {
					return []string{p.Id, p.Name, promptVisibility(p), compactDate(p.UpdatedAt), oneLine(p.Prompt)}
				},
				Items: prompts,
			}, nil
		}),
	}
}

func newPromptGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <promptId>",
		Short: "Show a custom prompt with its full text",
		Example: `  norcube langsync prompts get 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f
  norcube langsync prompts get 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f -o json --query prompt`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			prompts, err := listPrompts(cmd, c)
			if err != nil {
				return nil, err
			}
			id := strings.TrimSpace(args[0])
			for _, p := range prompts {
				if p.Id == id {
					return output.Object{Value: p, Fields: []output.Field{
						{Name: "ID", Value: p.Id},
						{Name: "Name", Value: p.Name},
						{Name: "Visibility", Value: promptVisibility(p)},
						{Name: "Created", Value: compactDate(p.CreatedAt)},
						{Name: "Updated", Value: compactDate(p.UpdatedAt)},
						{Name: "Prompt", Value: p.Prompt},
					}}, nil
				}
			}
			return nil, &clierr.Error{
				Code:    clierr.CodeNotFound,
				Message: fmt.Sprintf("no custom prompt with id %q", id),
				Hint:    "`norcube langsync prompts list` shows the prompts you can use; someone else's private prompts aren't listed.",
			}
		}),
	}
}

// promptText reads the prompt text from --text or --file.
func promptText(cmd *cobra.Command, text, file string) (string, error) {
	if cmd.Flags().Changed("text") && file != "" {
		return "", clierr.Usage(errors.New("pass --text or --file, not both"))
	}
	s, err := readText(cmd.InOrStdin(), text, file, "the prompt")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(s) == "" {
		return "", clierr.Usage(errors.New("the prompt text must not be empty"))
	}
	return s, nil
}

func newPromptCreateCmd() *cobra.Command {
	var (
		text, file string
		private    bool
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a custom prompt",
		Long: `Creates a custom prompt from --text, or from a file with --file (- reads
stdin). It is shared with the organization unless --private is set.`,
		Example: `  norcube langsync prompts create "Friendly tone" --text "Use an informal, friendly tone."
  norcube langsync prompts create "Legal" --file prompts/legal.txt --private`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return nil, clierr.Usage(errors.New("the prompt name must not be empty"))
			}
			if !cmd.Flags().Changed("text") && file == "" {
				return nil, clierr.Usage(errors.New("pass the prompt text with --text or --file"))
			}
			body, err := promptText(cmd, text, file)
			if err != nil {
				return nil, err
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.CreateCustomPromptWithResponse(cmd.Context(), langsync.TranslationCreateCustomPromptInput{
				Name: &name, Prompt: &body, IsPrivate: &private,
			})
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON500)
			}
			p := langsync.DtoCustomPrompt{Id: res.JSON200.Id, Name: name, Prompt: body, IsPrivate: private}
			return output.Done{
				Message: fmt.Sprintf("Created %s prompt %q (%s).\nUse it by default in a namespace with `norcube langsync namespaces update <name> --default-prompt %s`.",
					promptVisibility(p), name, p.Id, p.Id),
				Value:  map[string]any{"id": p.Id, "name": name, "isPrivate": private},
				Fields: []output.Field{{Value: p.Id}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&text, "text", "", "The prompt text")
	cmd.Flags().StringVar(&file, "file", "", "Read the prompt text from this file (- for stdin)")
	cmd.Flags().BoolVar(&private, "private", false, "Only you can see and use the prompt")
	return cmd
}

func newPromptUpdateCmd() *cobra.Command {
	var (
		name, text, file string
		private          bool
	)
	cmd := &cobra.Command{
		Use:   "update <promptId>",
		Short: "Rename a custom prompt, change its text or who can use it",
		Long: `Changes only what you pass. --private=false shares a private prompt
with the organization.`,
		Example: `  norcube langsync prompts update 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f --name "Friendly, informal"
  norcube langsync prompts update 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f --file prompts/tone.txt
  norcube langsync prompts update 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f --private=false`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id := strings.TrimSpace(args[0])
			var body langsync.TranslationPatchCustomPromptInput
			if cmd.Flags().Changed("name") {
				v := strings.TrimSpace(name)
				if v == "" {
					return nil, clierr.Usage(errors.New("--name must not be empty"))
				}
				body.Name = &v
			}
			if cmd.Flags().Changed("text") || file != "" {
				v, err := promptText(cmd, text, file)
				if err != nil {
					return nil, err
				}
				body.Prompt = &v
			}
			if cmd.Flags().Changed("private") {
				body.IsPrivate = &private
			}
			if body.Name == nil && body.Prompt == nil && body.IsPrivate == nil {
				return nil, clierr.Usage(errors.New("nothing to update: pass --name, --text, --file or --private"))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.UpdateCustomPromptWithResponse(cmd.Context(), id, body)
			if err != nil {
				return nil, err
			}
			if !ok2xx(res.HTTPResponse) {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON500)
			}
			return output.Done{
				Message: fmt.Sprintf("Updated prompt %s.", id),
				Value:   map[string]any{"id": id, "updated": true},
				Fields:  []output.Field{{Value: id}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&name, "name", "", "New name")
	cmd.Flags().StringVar(&text, "text", "", "New prompt text")
	cmd.Flags().StringVar(&file, "file", "", "Read the new prompt text from this file (- for stdin)")
	cmd.Flags().BoolVar(&private, "private", false, "Make the prompt private (true) or shared with the organization (false)")
	return cmd
}

func newPromptDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <promptId>",
		Aliases: []string{"rm"},
		Short:   "Delete a custom prompt",
		Long: `Deletes the prompt. Namespaces that used it by default fall back to no
custom prompt.`,
		Example: `  norcube langsync prompts delete 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f
  norcube langsync prompts delete 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f --yes   # scripts`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id := strings.TrimSpace(args[0])
			ok, err := cmdutil.Confirm(fmt.Sprintf("Delete custom prompt %s?", id), yes)
			if err != nil || !ok {
				return nil, cancelledOK(cmd, cancelledIfDeclined(err))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.DeleteCustomPromptWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if !ok2xx(res.HTTPResponse) {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON500)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted prompt %s.", id),
				Value:   deletedResult{ID: id, Deleted: true},
				Fields:  []output.Field{{Value: id}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a TTY)")
	return cmd
}

package cms

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cms-schema/load"
	"github.com/norcubeplatform/cms-schema/report"
	"github.com/norcubeplatform/cms-schema/validate"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// What `validate --fail-on` accepts, from the strictest to none.
var validateFailOn = []string{failOnWarning, failOnError, failOnNone}

const (
	failOnError   = "error"
	failOnWarning = "warning"
	failOnNone    = "none"
)

// validateScopes are the values of --only, in the order the checks run.
var validateScopes = []string{validate.ScopeSchema, validate.ScopeContent, validate.ScopeLinks}

// validateReportVersion is the version of the `cms validate -o json`
// document. Within a version changes are additive only.
const validateReportVersion = 1

// validateReport is what `cms validate -o json` prints.
type validateReport struct {
	SchemaVersion int `json:"schemaVersion" yaml:"schemaVersion"`
	// Path is the site directory as given on the command line.
	Path string `json:"path" yaml:"path"`
	// Findings are sorted by file, path and severity. Each names a rule
	// from the frozen list in the cms-schema module.
	Findings []report.Finding `json:"findings" yaml:"findings"`
	Summary  validateSummary  `json:"summary" yaml:"summary"`
}

// validateSummary counts what the run looked at and found.
type validateSummary struct {
	// Files is the number of content files checked, SchemaFiles the
	// number of schema files loaded.
	Files       int `json:"files" yaml:"files"`
	SchemaFiles int `json:"schemaFiles" yaml:"schemaFiles"`
	Errors      int `json:"errors" yaml:"errors"`
	Warnings    int `json:"warnings" yaml:"warnings"`
}

func newValidateCmd() *cobra.Command {
	var (
		only       []string
		collection string
		failOn     string
	)
	cmd := &cobra.Command{
		Use:         "validate [path]",
		Annotations: cmdutil.Reads(),
		Short:       "Check a site's CMS schema and content the way the editor does",
		Long: `Reads the site checkout at path (default: the current directory) and
reports what the Norcube CMS editor would: schema files that do not load
or contradict themselves, content that breaks a field's rules, and links,
images and references that point at nothing. Nothing is sent anywhere;
the command needs no sign-in.

Three groups of checks run, selectable with --only:
  schema   .norcube/cms/ loads: YAML, $ref, field types and knobs, select
           options, bounds, regexes, visibleIf paths, storages, preview,
           how a collection links its language versions (translations.by)
  content  every content file against its collection: required, value
           shapes, array item kinds and bounds, regex, length, uniqueness,
           custom validators, out-of-schema keys
  links    the site as a whole: two files at one URL, links no page
           produces, missing images and files, dangling references, two
           pages of one language sharing a translation key

Each finding names the file, the field path inside it, a severity and a
rule id (schema.unresolved_ref, content.required, links.missing_asset…).
Rule ids are stable; match on them in scripts.

The exit code follows --fail-on: with "error" (the default) the command
exits 1 when any finding is an error, with "warning" when any finding
exists, with "none" it always exits 0 and only reports. A path that is
not a CMS site exits 3.`,
		Example: `  norcube cms validate
  norcube cms validate ./site --fail-on warning
  norcube cms validate --only schema
  # In CI, keep the report as an artifact
  norcube cms validate -o json > cms-validate.json
  norcube cms validate -o json --query "findings[?rule=='links.missing_asset'].file"`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveFilterDirs
		},
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			for _, o := range only {
				if err := oneOf("--only", o, validateScopes); err != nil {
					return nil, err
				}
			}
			if err := oneOf("--fail-on", failOn, validateFailOn); err != nil {
				return nil, err
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			info, err := os.Stat(path)
			switch {
			case errors.Is(err, os.ErrNotExist):
				return nil, clierr.Usage(fmt.Errorf("%s: no such directory", path))
			case err != nil:
				return nil, err
			case !info.IsDir():
				return nil, clierr.Usage(fmt.Errorf("%s is a file; pass the site's directory", path))
			}

			res, err := validate.Site(os.DirFS(path), validate.Options{Only: only, Collection: collection})
			if errors.Is(err, load.ErrNotASite) {
				return nil, &clierr.Error{
					Code:    clierr.CodeNotFound,
					Message: fmt.Sprintf("%s holds no Norcube CMS site: no %s and no %s", path, load.ConfigFile, load.LegacyConfigFile),
					Hint:    "Run the command from the site's root, or pass its path.",
					Err:     err,
				}
			}
			if err != nil {
				return nil, err
			}

			rep := validateReport{
				SchemaVersion: validateReportVersion,
				Path:          path,
				Findings:      res.Findings,
				Summary:       validateSummary{Files: res.Files, SchemaFiles: res.SchemaFiles, Errors: res.Errors, Warnings: res.Warnings},
			}
			if rep.Findings == nil {
				rep.Findings = []report.Finding{}
			}
			view := output.View[report.Finding]{
				Value: rep,
				Table: output.Table[report.Finding]{
					Headers: []string{"SEVERITY", "FILE", "PATH", "RULE", "MESSAGE"},
					Rows: func(f report.Finding) []string {
						return []string{string(f.Severity), f.File, f.Path, string(f.Rule), f.Message}
					},
					Items: rep.Findings,
				},
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%d content %s, %d schema %s: %d %s, %d %s\n",
				res.Files, plural(res.Files, "file"), res.SchemaFiles, plural(res.SchemaFiles, "file"),
				res.Errors, plural(res.Errors, "error"), res.Warnings, plural(res.Warnings, "warning"))

			fail := (failOn == failOnError && res.Errors > 0) ||
				(failOn == failOnWarning && (res.Errors > 0 || res.Warnings > 0))
			if !fail {
				return view, nil
			}
			if err := cmdutil.Print(cmd, view); err != nil {
				return nil, err
			}
			return nil, &clierr.Error{
				Code:    clierr.CodeInvalid,
				Message: fmt.Sprintf("%s does not pass: %d %s, %d %s", path, res.Errors, plural(res.Errors, "error"), res.Warnings, plural(res.Warnings, "warning")),
				Hint:    "Fix the findings above, or pass --fail-on none to report without failing.",
				Details: rep.Summary,
			}
		}),
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "Run only these checks: schema, content, links (repeatable or comma-separated; default all)")
	cmd.Flags().StringVar(&collection, "collection", "", "Check the content and links of one collection only, by its key")
	cmd.Flags().StringVar(&failOn, "fail-on", failOnError, "Exit 1 on findings of this severity or worse: warning, error or none")
	cmdutil.FlagEnum(cmd, "only", validateScopes...)
	cmdutil.FlagEnum(cmd, "fail-on", validateFailOn...)
	return cmd
}

// oneOf rejects a flag value outside its allowed list before any work.
func oneOf(flag, v string, allowed []string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return clierr.Usage(fmt.Errorf("%s %q: one of %s", flag, v, strings.Join(allowed, ", ")))
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

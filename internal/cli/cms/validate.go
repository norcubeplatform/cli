package cms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cms-schema/gitlog"
	"github.com/norcubeplatform/cms-schema/load"
	"github.com/norcubeplatform/cms-schema/quality"
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

// validateScopes are the values of --only, in the order the checks run:
// every scope of the module except content quality, which --quality adds
// because it never runs unless asked for.
var validateScopes = slices.DeleteFunc(validate.Scopes(), func(s string) bool { return s == validate.ScopeQuality })

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
	// Quality says what the content quality checks looked at, skipped and
	// assumed; present only with --quality.
	Quality *quality.Summary `json:"quality,omitempty" yaml:"quality,omitempty"`
}

// validateSummary counts what the run looked at and found.
type validateSummary struct {
	// Files is the number of content files checked, SchemaFiles the
	// number of schema files loaded.
	Files       int `json:"files" yaml:"files"`
	SchemaFiles int `json:"schemaFiles" yaml:"schemaFiles"`
	Errors      int `json:"errors" yaml:"errors"`
	Warnings    int `json:"warnings" yaml:"warnings"`
	// Notices are low-impact or information-only quality findings; they
	// never fail a run.
	Notices int `json:"notices" yaml:"notices"`
}

// gitDates reads when each file of a checkout last changed
// (gitlog.LastChanged); a parameter so tests need no git.
type gitDates func(ctx context.Context, dir string, opts gitlog.Options) (map[string]time.Time, error)

// validateDeps are what `cms validate` reaches outside the site's files.
type validateDeps struct {
	// lastChanged runs for --git only.
	lastChanged gitDates
}

func newValidateCmd(deps validateDeps) *cobra.Command {
	var (
		only          []string
		collection    string
		failOn        string
		withQuality   bool
		strictQuality bool
		withGit       bool
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
           how a collection links its language versions (translations.by),
           field roles and image alt text
  content  every content file against its collection: required, value
           shapes, array item kinds and bounds, regex, length, uniqueness,
           custom validators, out-of-schema keys
  links    the site as a whole: two files at one URL, links no page
           produces, missing images and files, dangling references, two
           pages of one language sharing a translation key

--quality adds the content quality checks, which run only when asked
for: search (titles, descriptions, share images, noindex), accessibility
(alt text, heading levels, link text), languages (missing, incomplete and,
with --git, outdated translations) and media (heavy images, unused
files). Quality findings are never errors: high and medium impact ones are
warnings, low impact and information-only ones notices. --strict-quality
turns quality warnings into errors. --git reads when each file last
changed from the checkout's git history (git must be installed), so a
translation left behind its newer language version is found.

Each finding names the file, the field path inside it, a severity and a
rule id (schema.unresolved_ref, content.required, quality.title_missing…).
A finding about one language of a page that holds every language shows
the language after its path, as in "title.en [en]". Rule ids are stable;
match on them in scripts.

The exit code follows --fail-on: with "error" (the default) the command
exits 1 when any finding is an error, with "warning" when any finding is
a warning or an error, with "none" it always exits 0 and only reports.
Notices never fail a run. A path that is not a CMS site exits 3.`,
		Example: `  norcube cms validate
  norcube cms validate ./site --fail-on warning
  norcube cms validate --only schema
  # Also check titles, descriptions, alt text, languages and media
  norcube cms validate --quality
  # In CI: fail on high and medium impact quality findings, with outdated translations
  norcube cms validate --quality --git --strict-quality
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
			if withGit && !withQuality {
				return nil, clierr.Usage(errors.New("--git needs --quality: the dates are for the content quality checks"))
			}
			if strictQuality && !withQuality {
				return nil, clierr.Usage(errors.New("--strict-quality needs --quality"))
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

			site := os.DirFS(path)
			opts := validate.Options{Only: only, Collection: collection}
			if withQuality {
				if len(opts.Only) == 0 {
					opts.Only = slices.Clone(validateScopes)
				}
				opts.Only = append(opts.Only, validate.ScopeQuality)
				opts.StrictQuality = strictQuality
				opts.Quality = quality.Options{ListMedia: quality.FSMedia(site), RootFiles: quality.FSRootFiles(site)}
				if withGit {
					dates, err := readGitDates(cmd.Context(), path, deps.lastChanged)
					if err != nil {
						return nil, err
					}
					opts.Quality.LastChanged = dates
				}
			}

			res, err := validate.Site(site, opts)
			if errors.Is(err, load.ErrNotASite) {
				return nil, &clierr.Error{
					Code:    clierr.CodeNotFound,
					Message: fmt.Sprintf("%s holds no Norcube CMS site: no %s and no %s", path, load.ConfigFile, load.LegacyConfigFile),
					Hint:    notASiteHint(path, siteFoldersBelow(site, siteFolderDepth)),
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
				Summary: validateSummary{
					Files: res.Files, SchemaFiles: res.SchemaFiles,
					Errors: res.Errors, Warnings: res.Warnings, Notices: res.Notices,
				},
				Quality: res.Quality,
			}
			if rep.Findings == nil {
				rep.Findings = []report.Finding{}
			}
			view := output.View[report.Finding]{
				Value: rep,
				Table: output.Table[report.Finding]{
					Headers: []string{"SEVERITY", "FILE", "PATH", "RULE", "MESSAGE"},
					Rows: func(f report.Finding) []string {
						return []string{string(f.Severity), f.File, findingPath(f), string(f.Rule), f.Message}
					},
					Items: rep.Findings,
				},
			}
			stderr := cmd.ErrOrStderr()
			counts := fmt.Sprintf("%d %s, %d %s", res.Errors, plural(res.Errors, "error"), res.Warnings, plural(res.Warnings, "warning"))
			if withQuality || res.Notices > 0 {
				counts += fmt.Sprintf(", %d %s", res.Notices, plural(res.Notices, "notice"))
			}
			fmt.Fprintf(stderr, "%d content %s, %d schema %s: %s\n",
				res.Files, plural(res.Files, "file"), res.SchemaFiles, plural(res.SchemaFiles, "file"), counts)
			if res.Quality != nil && cmdutil.Human(cmd) {
				printQualityNotes(stderr, res.Quality, withGit)
			}

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
	cmd.Flags().StringVar(&collection, "collection", "", "Check the content, links and quality of one collection only, by its key")
	cmd.Flags().StringVar(&failOn, "fail-on", failOnError, "Exit 1 on findings of this severity or worse: warning, error or none")
	cmd.Flags().BoolVar(&withQuality, "quality", false, "Also run the content quality checks (search, accessibility, languages, media); their findings are warnings and notices")
	cmd.Flags().BoolVar(&strictQuality, "strict-quality", false, "With --quality, treat content quality warnings as errors; notices stay notices")
	cmd.Flags().BoolVar(&withGit, "git", false, "With --quality, read when each file last changed from the checkout's git history, so outdated translations are found")
	cmdutil.FlagEnum(cmd, "only", validateScopes...)
	cmdutil.FlagEnum(cmd, "fail-on", validateFailOn...)
	return cmd
}

// readGitDates runs read on the checkout at path with a deadline
// (gitlog.DefaultTimeout) and turns its failures into errors a person can
// act on. --git asked for the dates, so a failure ends the run rather than
// silently skipping the check that needs them.
func readGitDates(ctx context.Context, path string, read gitDates) (map[string]time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, gitlog.DefaultTimeout)
	defer cancel()
	dates, err := read(ctx, path, gitlog.Options{})
	switch {
	case err == nil:
		return dates, nil
	case errors.Is(err, context.Canceled):
		// Interrupted: the root reports it as such.
		return nil, err
	case errors.Is(err, gitlog.ErrNotARepository):
		return nil, &clierr.Error{
			Code:    clierr.CodeUsage,
			Message: fmt.Sprintf("--git: %s is not in a git checkout, or git is not installed", path),
			Hint:    "Run it on a clone of the site's repository with git on the PATH, or drop --git.",
			Err:     err,
		}
	case errors.Is(err, context.DeadlineExceeded):
		return nil, &clierr.Error{
			Code:    clierr.CodeTimeout,
			Message: fmt.Sprintf("--git: reading the git history of %s took longer than %s", path, gitlog.DefaultTimeout),
			Hint:    "Drop --git to check without the dates; outdated translations are then not looked for.",
			Err:     err,
		}
	default:
		return nil, &clierr.Error{
			Code:    clierr.CodeError,
			Message: fmt.Sprintf("--git: reading the git history of %s: %v", path, err),
			Hint:    "Drop --git to check without the dates; outdated translations are then not looked for.",
			Err:     err,
		}
	}
}

// findingPath is the PATH cell: the field path, followed by the language
// when the finding is about one language of a page that holds every
// language, so per-language findings tell apart.
func findingPath(f report.Finding) string {
	if f.Locale == "" {
		return f.Path
	}
	if f.Path == "" {
		return "[" + f.Locale + "]"
	}
	return f.Path + " [" + f.Locale + "]"
}

// printQualityNotes tells a person what the quality checks could not do
// and what they assumed, for the table output only (-o json carries the
// same in "quality").
func printQualityNotes(w io.Writer, s *quality.Summary, withGit bool) {
	for _, sk := range s.Skipped {
		note := sk.Reason
		if sk.Rule == report.RuleQualityTranslationOutdated && !withGit {
			note += "; pass --git to read the dates from the git history"
		}
		fmt.Fprintf(w, "skipped %s: %s\n", sk.Rule, note)
	}
	for _, g := range s.Guessed {
		fmt.Fprintf(w, "guessed %s: %t for %s (storage %s, %s site); set it on the storage to confirm\n",
			g.Kind, g.Value, g.Path, g.Storage, g.Framework)
	}
	if s.Drafts > 0 {
		fmt.Fprintf(w, "%d draft %s not checked for search and accessibility\n", s.Drafts, plural(s.Drafts, "page"))
	}
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

// siteFolderDepth is how deep below the path a site is looked for when
// the path holds none: a monorepo keeps its site in a folder (website/)
// or a folder of a folder (apps/web/).
const siteFolderDepth = 2

// siteFoldersBelow lists the folders of fsys, at most depth levels deep,
// that hold a Norcube CMS site (load.ConfigFile or load.LegacyConfigFile),
// in name order. Hidden folders and node_modules are not entered.
func siteFoldersBelow(fsys fs.FS, depth int) []string {
	var out []string
	var visit func(dir string, level int)
	visit = func(dir string, level int) {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || name == "node_modules" {
				continue
			}
			child := name
			if dir != "." {
				child = dir + "/" + name
			}
			for _, marker := range []string{load.ConfigFile, load.LegacyConfigFile} {
				if info, err := fs.Stat(fsys, child+"/"+marker); err == nil && !info.IsDir() {
					out = append(out, child)
					break
				}
			}
			if level < depth {
				visit(child, level+1)
			}
		}
	}
	visit(".", 1)
	return out
}

// notASiteHint tells where a site is when the path holds none but a folder
// below it does (a site in a subfolder of a monorepo).
func notASiteHint(path string, folders []string) string {
	if len(folders) == 0 {
		return "Run the command from the site's root, or pass its path."
	}
	where := filepath.ToSlash(filepath.Join(path, folders[0]))
	if len(folders) == 1 {
		return fmt.Sprintf("The site is in %s/: run nrc cms validate %s", folders[0], where)
	}
	return fmt.Sprintf("Sites are in %s/: pass one, for example nrc cms validate %s", strings.Join(folders, "/, "), where)
}

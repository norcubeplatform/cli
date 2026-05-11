package langsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
)

func newInitCmd() *cobra.Command {
	var (
		dirFlag    string
		nsFlags    []string
		forceWrite bool
		seedFlag   string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a .langsync.json in the project root so this codebase can sync with Langsync",
		Long: `Sets up Langsync inside the current project. Run this once per
codebase; the resulting .langsync.json is committed alongside your
source so every dev (and CI) syncs against the same backend
namespaces.

The wizard:
  1. lists every namespace in the active organization,
  2. lets you pick one or more,
  3. for each pick, asks where its translation files live on disk
     (default: i18n/<namespace>), and
  4. fetches the namespace's backend default language so sync knows
     which local file to push from.

Re-run any time to add a new namespace; existing entries are kept
untouched (use --force to overwrite the file from scratch).

After writing the config, init runs a follow-up action controlled by
--seed:
  pull (default) — download the server's current state to disk.
                   Right when the team has been editing strings in
                   the web app and a fresh checkout needs them.
  push           — full sync: send local files to the server,
                   autotranslate the rest, write the result back to
                   disk. Right when this project already has JSON
                   files and the namespace on the server is empty
                   (or you want local to win the merge).
  none           — write the config and stop.

Either seed mode only applies to newly-added namespaces; entries
already in .langsync.json from a previous init are left alone.
--force counts every picked namespace as new.

Examples:
  norcube langsync init
  norcube langsync init -n web -n marketing --dir i18n
  norcube langsync init --force
  norcube langsync init --seed push          # use my local JSON files as the source of truth
  norcube langsync init --seed none          # config only, do nothing else`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}

			// We always write the config to the current working
			// directory. Walking up to find an existing one would
			// surprise users who run `init` from a subdir expecting
			// to start a new project there.
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			cfgPath := filepath.Join(cwd, projectcfg.Filename)

			existing, existingErr := loadExistingForInit(cfgPath, forceWrite)
			if existingErr != nil {
				return existingErr
			}

			// Fetch namespaces + the org-wide language list once so we
			// can resolve DefaultLanguageId → code without an N+1.
			namespaces, err := fetchAllNamespaces(cmd.Context(), c)
			if err != nil {
				return err
			}
			if len(namespaces) == 0 {
				return fmt.Errorf("no namespaces in the active organization — create one with `norcube langsync namespace create` first")
			}
			langByID, err := fetchLanguageCodesByID(cmd.Context(), c)
			if err != nil {
				return err
			}

			picked, err := resolveInitNamespaceSelection(namespaces, nsFlags, existing)
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}
			if len(picked) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing selected. Nothing written.")
				return nil
			}

			additions, err := buildInitEntries(picked, langByID, existing, dirFlag)
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			final := mergeInitEntries(existing, additions, forceWrite)
			final.Version = projectcfg.CurrentVersion

			if err := projectcfg.Save(cfgPath, final); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			rel, _ := filepath.Rel(cwd, cfgPath)
			if rel == "" {
				rel = cfgPath
			}
			fmt.Fprintf(out, "Wrote %s with %d namespace(s):\n", rel, len(final.Namespaces))
			for _, ns := range final.Namespaces {
				fmt.Fprintf(out, "  • %s  (dir: %s, default local lang: %s)\n", ns.Namespace, ns.Dir, ns.DefaultLocalLanguage)
			}

			// Seed pass: only run on newly-added namespaces.
			// Already-configured entries are assumed in sync;
			// --force flips that and treats every picked namespace
			// as new. Failures here don't roll back the config save
			// — the user can rerun the appropriate command later.
			seed, err := parseSeedMode(seedFlag)
			if err != nil {
				return err
			}
			toSeed := newlyAddedNamespaces(additions, existing, forceWrite)
			switch {
			case seed == seedModeNone:
				fmt.Fprintln(out, "\nSkipped seed (--seed none). Run `norcube langsync pull` or `sync` when you're ready.")
			case len(toSeed) == 0:
				fmt.Fprintln(out, "\nNo newly-added namespaces to seed.")
			case seed == seedModePull:
				fmt.Fprintf(out, "\nPulling current server state for %d namespace(s):\n", len(toSeed))
				if err := runInitSeed(cmd, c, final, cfgPath, toSeed, strategyServer); err != nil {
					fmt.Fprintf(out, "Pull encountered errors — run `norcube langsync pull` to retry.\n")
					return err
				}
			case seed == seedModePush:
				fmt.Fprintf(out, "\nPushing local files for %d namespace(s) and waiting for autotranslate:\n", len(toSeed))
				if err := runInitSeed(cmd, c, final, cfgPath, toSeed, strategyLocal); err != nil {
					fmt.Fprintf(out, "Push encountered errors — run `norcube langsync sync` to retry.\n")
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dirFlag, "dir", "", "Parent directory for translation files (each picked namespace becomes <dir>/<namespace>); skips the per-namespace dir prompt")
	cmd.Flags().StringSliceVarP(&nsFlags, "namespace", "n", nil, "Namespace name to include (repeat for multiple); skips the picker")
	cmd.Flags().BoolVar(&forceWrite, "force", false, "Overwrite an existing .langsync.json instead of merging")
	cmd.Flags().StringVar(&seedFlag, "seed", string(seedModePull), "After writing the config: pull (download server state), push (upload local files + autotranslate), or none (do nothing)")
	return cmd
}

// newlyAddedNamespaces returns the subset of additions that wasn't
// already in the existing config. With --force every addition
// counts as new (the existing file is being discarded). With no
// existing config (first-time init), all additions are new.
func newlyAddedNamespaces(additions []projectcfg.Namespace, existing *projectcfg.File, force bool) []projectcfg.Namespace {
	if force || existing == nil {
		return additions
	}
	have := map[string]bool{}
	for _, ns := range existing.Namespaces {
		have[ns.Namespace] = true
	}
	var out []projectcfg.Namespace
	for _, ns := range additions {
		if !have[ns.Namespace] {
			out = append(out, ns)
		}
	}
	return out
}

// seedMode controls what init does AFTER writing the config: pull
// the server state, push local files, or nothing. See the --seed
// flag for what each one means in user-facing terms.
type seedMode string

const (
	seedModePull seedMode = "pull"
	seedModePush seedMode = "push"
	seedModeNone seedMode = "none"
)

func parseSeedMode(s string) (seedMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "pull":
		return seedModePull, nil
	case "push":
		return seedModePush, nil
	case "none", "skip", "off":
		return seedModeNone, nil
	}
	return "", fmt.Errorf("invalid --seed %q (must be pull|push|none)", s)
}

// runInitSeed drives either the pull or push seed pass. Both use
// runParallelSync; only the strategy differs. Failures are reported
// but don't roll back the config — the user can always rerun the
// appropriate command later.
func runInitSeed(cmd *cobra.Command, c *langsyncContext, cfg *projectcfg.File, cfgPath string, toSeed []projectcfg.Namespace, strat syncStrategy) error {
	return runParallelSync(cmd, c, cfg, cfgPath, toSeed, syncOptions{
		strategy:    strat,
		wait:        true,
		waitTimeout: 5 * time.Minute,
		pollEvery:   1 * time.Second,
	})
}

// loadExistingForInit reads any pre-existing config at path. With
// --force the existing file is ignored (we'll overwrite it later); a
// missing file is treated as an empty starting point.
func loadExistingForInit(path string, force bool) (*projectcfg.File, error) {
	if force {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return projectcfg.Load(path)
}

func fetchAllNamespaces(ctx context.Context, c *langsyncContext) ([]langsync.DtoDTONamespace, error) {
	res, err := c.client.ListNamespacesWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, apiError(res.HTTPResponse, res.Body,
			res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
	}
	return *res.JSON200, nil
}

// fetchLanguageCodesByID returns a lookup table of language id → code
// across every shared and custom language visible to the active org.
// Used by init to render namespace.DefaultLanguageId as a code on
// disk; the code is the thing that maps cleanly to a filename.
func fetchLanguageCodesByID(ctx context.Context, c *langsyncContext) (map[int]string, error) {
	res, err := c.client.ListLanguagesForOrgWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, apiError(res.HTTPResponse, res.Body, res.JSON401, res.JSON500)
	}
	out := map[int]string{}
	for _, l := range *res.JSON200 {
		if l.Id != nil && l.Code != nil {
			out[*l.Id] = *l.Code
		}
	}
	return out, nil
}

// resolveInitNamespaceSelection turns either -n flags or the
// interactive multi-select into a slice of DtoDTONamespace pointing
// at the picked rows. Already-configured namespaces are preselected
// in the picker (so re-running init defaults to "keep what I have").
func resolveInitNamespaceSelection(all []langsync.DtoDTONamespace, flags []string, existing *projectcfg.File) ([]langsync.DtoDTONamespace, error) {
	byName := map[string]langsync.DtoDTONamespace{}
	for _, ns := range all {
		if ns.Name != nil && *ns.Name != "" {
			byName[*ns.Name] = ns
		}
	}

	if len(flags) > 0 {
		var picked []langsync.DtoDTONamespace
		for _, name := range flags {
			name = strings.TrimSpace(name)
			ns, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("namespace %q not found in the active organization", name)
			}
			picked = append(picked, ns)
		}
		return picked, nil
	}

	if !stdinIsInteractive() {
		return nil, fmt.Errorf("pass --namespace <name> one or more times — interactive picker requires a TTY")
	}

	alreadyConfigured := map[string]bool{}
	if existing != nil {
		for _, ns := range existing.Namespaces {
			alreadyConfigured[ns.Namespace] = true
		}
	}

	opts := make([]huh.Option[string], 0, len(all))
	preselected := []string{}
	for _, ns := range all {
		name := ""
		if ns.Name != nil {
			name = *ns.Name
		}
		if name == "" {
			continue
		}
		label := name
		if ns.Context != nil && *ns.Context != "" {
			label = fmt.Sprintf("%s — %s", name, *ns.Context)
		}
		if alreadyConfigured[name] {
			label += "  (already configured)"
		}
		opt := huh.NewOption(label, name)
		if alreadyConfigured[name] {
			preselected = append(preselected, name)
		}
		opts = append(opts, opt)
	}
	if len(opts) == 0 {
		return nil, fmt.Errorf("no namespaces available to pick")
	}

	selected := preselected
	err := huh.NewMultiSelect[string]().
		Title("Which namespaces should this project sync?").
		Description("Space to toggle, Enter to confirm. You can re-run `init` later to add more.").
		Options(opts...).
		Value(&selected).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, ErrCancelled
		}
		return nil, err
	}

	var picked []langsync.DtoDTONamespace
	for _, name := range selected {
		picked = append(picked, byName[name])
	}
	return picked, nil
}

// buildInitEntries fills in the per-namespace fields (dir, format,
// default_local_language). Pre-existing entries supply their dir as
// the default suggestion so a re-run doesn't shuffle paths around.
func buildInitEntries(picked []langsync.DtoDTONamespace, langByID map[int]string, existing *projectcfg.File, dirFlag string) ([]projectcfg.Namespace, error) {
	existingByName := map[string]projectcfg.Namespace{}
	if existing != nil {
		for _, ns := range existing.Namespaces {
			existingByName[ns.Namespace] = ns
		}
	}

	out := make([]projectcfg.Namespace, 0, len(picked))
	for _, ns := range picked {
		name := ""
		if ns.Name != nil {
			name = *ns.Name
		}
		if name == "" {
			return nil, fmt.Errorf("backend returned a namespace without a name; refusing to write a broken config")
		}

		defaultCode := ""
		if ns.DefaultLanguageId != nil {
			defaultCode = langByID[*ns.DefaultLanguageId]
		}
		if defaultCode == "" {
			return nil, fmt.Errorf("namespace %q has no default language attached on the server — set one in the web app first", name)
		}

		dir := suggestNamespaceDir(name, dirFlag, existingByName[name])
		if dirFlag == "" && stdinIsInteractive() {
			// Ask the user to confirm or change the suggested dir.
			input := dir
			err := huh.NewInput().
				Title(fmt.Sprintf("Directory for namespace %q", name)).
				Description("Where this namespace's per-language JSON files live (relative to .langsync.json).").
				Value(&input).
				Run()
			if err != nil {
				if errors.Is(err, huh.ErrUserAborted) {
					return nil, ErrCancelled
				}
				return nil, err
			}
			dir = strings.TrimSpace(input)
			if dir == "" {
				return nil, fmt.Errorf("dir for namespace %q must not be empty", name)
			}
		}

		out = append(out, projectcfg.Namespace{
			Namespace:            name,
			Dir:                  dir,
			Format:               projectcfg.FormatFlatJSON,
			DefaultLocalLanguage: defaultCode,
		})
	}
	return out, nil
}

func suggestNamespaceDir(name, dirFlag string, prior projectcfg.Namespace) string {
	// Preserve the user's prior choice unless they explicitly passed
	// --dir to reshape the layout.
	if prior.Dir != "" && dirFlag == "" {
		return prior.Dir
	}
	parent := dirFlag
	if parent == "" {
		parent = "i18n"
	}
	return filepath.ToSlash(filepath.Join(parent, name))
}

// mergeInitEntries combines existing entries with new ones from the
// current init run. Names present in additions replace whatever was
// in existing (so the user can change a dir by re-running init).
// With --force the existing file is discarded entirely.
func mergeInitEntries(existing *projectcfg.File, additions []projectcfg.Namespace, force bool) *projectcfg.File {
	out := &projectcfg.File{Version: projectcfg.CurrentVersion}
	addByName := map[string]projectcfg.Namespace{}
	for _, ns := range additions {
		addByName[ns.Namespace] = ns
	}
	if !force && existing != nil {
		for _, ns := range existing.Namespaces {
			if replaced, ok := addByName[ns.Namespace]; ok {
				out.Namespaces = append(out.Namespaces, replaced)
				delete(addByName, ns.Namespace)
				continue
			}
			out.Namespaces = append(out.Namespaces, ns)
		}
	}
	// Append the rest of the additions (those that weren't replacing).
	// We preserve `additions` order for the new ones so the file
	// reflects the order the user picked them.
	for _, ns := range additions {
		if _, still := addByName[ns.Namespace]; still {
			out.Namespaces = append(out.Namespaces, ns)
		}
	}
	return out
}

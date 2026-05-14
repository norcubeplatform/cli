package langsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg/translations"
)

// syncStrategy mirrors the --strategy flag. "interactive" runs
// purely client-side: it walks every default-language conflict
// with the server BEFORE submitting, then sends a "local" job
// with the user's resolved values. The backend only ever sees
// "local" or "server".
type syncStrategy string

const (
	strategyLocal       syncStrategy = "local"
	strategyServer      syncStrategy = "server"
	strategyInteractive syncStrategy = "interactive"
)

func newSyncCmd() *cobra.Command {
	var (
		dryRun                    bool
		strategy                  string
		nsFilters                 []string
		configPath                string
		prune                     bool
		wait                      bool
		waitTimeout               time.Duration
		pollEvery                 time.Duration
		retranslateOnSourceChange bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile a project's local translation files with Langsync",
		Long: `Reconciles the local translation files in .langsync.json against
the configured Langsync namespaces. The CLI submits one sync job per
namespace and polls until the server is done; all namespaces run in
parallel so you see a live dashboard of every job at once.

Phases (driven by the backend, durable across backend restarts):
  1. planning — diff submitted marks vs server state, write op list
  2. pushing  — apply each op (idempotent on resume)
  3. autotranslating — trigger LLM batch (at-most-once cost guarantee)
  4. finalizing — read per-language state and return it

Flags:
  --dry-run            stop after planning; the response carries the plan
  --strategy local     (default) push local, local-wins on conflicts
  --strategy server    skip push entirely (pull-only refresh)
  --strategy interactive  per-conflict prompt before submitting
  --prune              delete server-side marks missing locally
  --wait               (default) block until autotranslate has drained
  --wait=false         submit and return; translations finish in the background
  --retranslate-on-source-change
                       when a term's source-language value has changed,
                       empty its existing non-source-language translations
                       so the server re-translates them via the LLM
                       (default off: stale translations stay as-is)

Examples:
  norcube langsync sync
  norcube langsync sync --dry-run
  norcube langsync sync -n ytracker-backend
  norcube langsync sync --strategy server
  norcube langsync sync --strategy interactive
  norcube langsync sync --prune
  norcube langsync sync --wait=false   # don't block on autotranslate`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			path, cfg, err := loadProjectConfig(configPath)
			if err != nil {
				return err
			}
			strat, err := parseStrategy(strategy)
			if err != nil {
				return err
			}
			targets, err := selectSyncTargets(cfg, nsFilters)
			if err != nil {
				return err
			}
			opts := syncOptions{
				strategy:                  strat,
				dryRun:                    dryRun,
				prune:                     prune,
				wait:                      wait,
				waitTimeout:               waitTimeout,
				pollEvery:                 pollEvery,
				retranslateOnSourceChange: retranslateOnSourceChange,
			}
			return runParallelSync(cmd, c, cfg, path, targets, opts)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Submit a planning-only job (returns the diff without applying it)")
	cmd.Flags().StringVar(&strategy, "strategy", string(strategyLocal), "Conflict policy: local|server|interactive")
	cmd.Flags().StringSliceVarP(&nsFilters, "namespace", "n", nil, "Sync only this namespace (repeat for multiple)")
	cmd.Flags().StringVar(&configPath, "config", "", "Path to .langsync.json (defaults to walking up from the cwd)")
	cmd.Flags().BoolVar(&prune, "prune", false, "Delete server-side marks that aren't present in the local default-language file")
	cmd.Flags().BoolVar(&wait, "wait", true, "Block until autotranslate has filled every gap (pass --wait=false to skip)")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "Client-side timeout for the polling loop")
	cmd.Flags().DurationVar(&pollEvery, "poll-every", 1*time.Second, "How often to poll the job's state")
	cmd.Flags().BoolVar(&retranslateOnSourceChange, "retranslate-on-source-change", false, "Re-translate non-source values when a term's source-language value has changed (overwrites stale translations; preserves the source-of-truth-is-source-lang invariant)")
	return cmd
}

// syncOptions bundles per-run flags so per-namespace fns don't grow
// without bound.
type syncOptions struct {
	strategy    syncStrategy
	dryRun      bool
	prune       bool
	wait        bool
	waitTimeout time.Duration
	pollEvery   time.Duration

	// pushDefaultOnly limits the push to the default-language file
	// (legacy behavior). When false (the default for `sync`),
	// every <lang>.json file in the namespace dir is submitted, so
	// human-edited translations in non-default langs are preserved
	// instead of getting blasted by autotranslate.
	pushDefaultOnly bool

	// retranslateOnSourceChange tells the server to invalidate
	// existing non-source-language translations whose source-lang
	// value changed in this sync, so the autotranslate phase refills
	// them via the LLM. Without this, a source-value edit leaves the
	// other languages' translations stale (semantically out of date
	// vs the new source). Opt-in to avoid blasting hand-curated
	// translations on a casual sync.
	retranslateOnSourceChange bool
}

// runParallelSync is the shared entry point for both `sync` and the
// init auto-pull path. It owns the dashboard lifecycle (start/close)
// and the goroutine fan-out.
func runParallelSync(cmd *cobra.Command, c *langsyncContext, cfg *projectcfg.File, configPath string, targets []projectcfg.Namespace, opts syncOptions) error {
	if len(targets) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Nothing to do — no namespaces configured.")
		return nil
	}

	// Pre-flight: detect local <code>.json files for languages
	// that aren't attached to the namespace yet. Without this, the
	// backend's planner would silently drop those values. The
	// pre-flight prompts interactively to attach (or create+attach
	// a custom lang) before the dashboard starts; non-TTY runs get
	// a warning per skipped file instead. Errors here are fatal —
	// the user almost certainly wants to fix the situation before
	// committing to the sync.
	if opts.strategy != strategyServer {
		if err := preflightAttachLanguages(cmd.Context(), c, cfg, configPath, targets); err != nil {
			return err
		}
	}

	names := make([]string, 0, len(targets))
	for _, ns := range targets {
		names = append(names, ns.Namespace)
	}
	d := newDashboard(cmd.OutOrStdout(), names)
	d.Start()

	// One goroutine per namespace. Each returns a result row that
	// feeds the post-run issue/summary block.
	results := make([]syncResult, len(targets))
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ns := targets[i]
			stats, job, err := syncNamespaceParallel(cmd.Context(), c, d, cfg, configPath, ns, opts)
			results[i] = syncResult{namespace: ns.Namespace, stats: stats, err: err, finalJob: job}
		}(i)
	}
	wg.Wait()
	d.Close()

	// Issues block: per-namespace failure detail with actionable
	// hints (especially the "X cells still empty" common case,
	// which usually means an empty source value the LLM had
	// nothing to translate from). The dashboard rows above are the
	// per-namespace summary; we don't print a separate Summary
	// block since that would duplicate them line-for-line.
	issues := collectIssues(cmd.Context(), c, cfg, configPath, results)
	if len(issues) > 0 {
		d.PrintHeading("Issues")
		for _, line := range issues {
			d.PrintNote(line)
		}
	}

	if opts.dryRun {
		d.PrintHeading("Dry run — nothing was changed")
	}

	anyErr := false
	for _, r := range results {
		if r.err != nil {
			anyErr = true
			break
		}
	}
	if anyErr {
		return fmt.Errorf("one or more namespaces failed to sync")
	}
	return nil
}

func loadProjectConfig(override string) (string, *projectcfg.File, error) {
	path := override
	if path == "" {
		var err error
		path, err = projectcfg.Find("")
		if err != nil {
			return "", nil, err
		}
	}
	cfg, err := projectcfg.Load(path)
	if err != nil {
		return "", nil, err
	}
	return path, cfg, nil
}

func parseStrategy(s string) (syncStrategy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "local", "":
		return strategyLocal, nil
	case "server":
		return strategyServer, nil
	case "interactive":
		if !stdinIsInteractive() {
			return "", fmt.Errorf("--strategy interactive needs a TTY (stdin isn't a terminal)")
		}
		return strategyInteractive, nil
	default:
		return "", fmt.Errorf("invalid --strategy %q (must be local|server|interactive)", s)
	}
}

func selectSyncTargets(cfg *projectcfg.File, filters []string) ([]projectcfg.Namespace, error) {
	if len(filters) == 0 {
		return cfg.Namespaces, nil
	}
	want := map[string]bool{}
	for _, n := range filters {
		want[strings.TrimSpace(n)] = true
	}
	var out []projectcfg.Namespace
	for _, ns := range cfg.Namespaces {
		if want[ns.Namespace] {
			out = append(out, ns)
			delete(want, ns.Namespace)
		}
	}
	if len(want) > 0 {
		var missing []string
		for n := range want {
			missing = append(missing, n)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("namespace(s) not in .langsync.json: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// syncNamespaceParallel is what each goroutine runs. It reports
// progress via the dashboard (Update/Complete/Fail) rather than
// printing directly, so all namespaces share one rendering region.
func syncNamespaceParallel(
	ctx context.Context,
	c *langsyncContext,
	d *dashboard,
	cfg *projectcfg.File,
	configPath string,
	ns projectcfg.Namespace,
	opts syncOptions,
) (namespaceSyncStats, *langsync.DtoDTOSyncJob, error) {
	dir := cfg.ResolveDir(configPath, ns)

	marksPerLang, totalCells, err := collectLocalMarks(dir, ns.DefaultLocalLanguage, ns.LanguageAliases, opts.pushDefaultOnly)
	if err != nil {
		d.Fail(ns.Namespace, fmt.Errorf("read local files: %w", err))
		return namespaceSyncStats{}, nil, err
	}
	d.Update(ns.Namespace, "reading", 0, 0,
		fmt.Sprintf("%d local marks across %d language file(s) in %s",
			totalCells, len(marksPerLang), relPath(configPath, dir)))

	// Interactive resolution runs serially per namespace (the user
	// can't realistically answer multiple namespaces' prompts at
	// once). Scoped to the default lang — non-default conflicts are
	// pushed local-wins without a prompt (matches the user intent of
	// "my files are the truth for human translations").
	if opts.strategy == strategyInteractive {
		defaultMarks := marksPerLang[ns.DefaultLocalLanguage]
		resolved, err := interactivelyResolveConflicts(ctx, c, ns.Namespace, defaultMarks, ns.DefaultLocalLanguage)
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				d.Complete(ns.Namespace, "aborted interactive resolution")
				return namespaceSyncStats{}, nil, nil
			}
			d.Fail(ns.Namespace, err)
			return namespaceSyncStats{}, nil, err
		}
		marksPerLang[ns.DefaultLocalLanguage] = resolved
	}

	d.Update(ns.Namespace, "submitting", 0, 0, "")
	jobID, err := submitSyncJob(ctx, c, ns, opts, marksPerLang)
	if err != nil {
		d.Fail(ns.Namespace, fmt.Errorf("submit job: %w", err))
		return namespaceSyncStats{}, nil, err
	}

	final, err := pollSyncJobDashboard(ctx, c, d, ns.Namespace, jobID, opts)
	if err != nil {
		d.Fail(ns.Namespace, err)
		return namespaceSyncStats{}, final, err
	}

	stats := statsFromJob(final)

	if !opts.dryRun && final != nil && final.Status != nil && *final.Status == "completed" {
		written, werr := writeResultFiles(final, dir, configPath, ns.LanguageAliases)
		if werr != nil {
			d.Fail(ns.Namespace, fmt.Errorf("write files: %w", werr))
			return stats, final, werr
		}
		stats.filesWritten = written
	}

	d.Complete(ns.Namespace, summarizeForFinishLine(final, stats.filesWritten))
	return stats, final, nil
}

// collectLocalMarks reads the namespace's on-disk translation files
// and returns a map[server-lang-code]map[mark]value plus the total
// cell count.
//
// aliases applies the forward mapping (disk filename code → server
// language code). For example: a file `cs_cz.json` with the alias
// `{"cs_cz": "cs-CZ"}` reads into the result keyed by "cs-CZ", which
// is what Langsync's backend knows the language as. Without an
// alias entry the disk code is used as-is.
//
// When pushDefaultOnly is true, only the default-language file is
// read — the `--seed push-default` mode for users who want AI to
// re-translate everything else from scratch.
//
// Missing files are tolerated: namespaces with no on-disk language
// file get an empty submission for that lang ("no opinion"), and
// the server's planner skips silently.
func collectLocalMarks(dir, defaultCode string, aliases map[string]string, pushDefaultOnly bool) (map[string]map[string]string, int, error) {
	out := map[string]map[string]string{}
	total := 0

	defaultPath := filepath.Join(dir, translations.LangFileName(defaultCode))
	defaultMarks, err := translations.ReadFlatJSON(defaultPath)
	switch {
	case err == nil:
		out[serverLangCode(defaultCode, aliases)] = defaultMarks
		total += len(defaultMarks)
	case errors.Is(err, os.ErrNotExist):
		out[serverLangCode(defaultCode, aliases)] = map[string]string{}
	default:
		return nil, 0, err
	}

	if pushDefaultOnly {
		return out, total, nil
	}

	codes, paths, err := translations.ListLangsInDir(dir)
	if err != nil {
		return nil, 0, err
	}
	for i, code := range codes {
		if code == defaultCode {
			continue
		}
		marks, err := translations.ReadFlatJSON(paths[i])
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] warning: skipping %s: %v\n", code, paths[i], err)
			continue
		}
		out[serverLangCode(code, aliases)] = marks
		total += len(marks)
	}
	return out, total, nil
}

// serverLangCode resolves a disk filename code to the server-side
// language code, applying the forward alias map. The function
// signature is deliberately tiny so callers don't have to nil-check
// the alias map at every call site.
func serverLangCode(diskCode string, aliases map[string]string) string {
	if aliases != nil {
		if mapped, ok := aliases[diskCode]; ok && mapped != "" {
			return mapped
		}
	}
	return diskCode
}

func submitSyncJob(ctx context.Context, c *langsyncContext, ns projectcfg.Namespace, opts syncOptions, marksPerLang map[string]map[string]string) (string, error) {
	serverStrategy := "local"
	if opts.strategy == strategyServer {
		serverStrategy = "server"
	}
	mpl := marksPerLang
	// The wire's default_language_code is the SERVER-side language
	// code (used by the backend's resolveSourceLanguageOverride to
	// look up the attached language by code). If the project's
	// local default is aliased to a different server-side code
	// (e.g. "czech" on disk → "cs" on the server), apply the alias
	// here so the backend gets the code it actually knows. Without
	// this, a non-aliased local code like "czech" reaches the
	// server and the source-override lookup fails with "isn't
	// attached".
	defaultCode := serverLangCode(ns.DefaultLocalLanguage, ns.LanguageAliases)
	body := langsync.SyncjobCreateSyncRequest{
		DefaultLanguageCode:       ptrStr(defaultCode),
		DryRun:                    ptrBool(opts.dryRun),
		MarksPerLanguage:          &mpl,
		Prune:                     ptrBool(opts.prune),
		Strategy:                  ptrStr(serverStrategy),
		WaitForAutotranslate:      ptrBool(opts.wait),
		RetranslateOnSourceChange: ptrBool(opts.retranslateOnSourceChange),
	}
	res, err := c.client.PostNamespacesNamespaceNameSyncWithResponse(ctx, ns.Namespace, body)
	if err != nil {
		return "", err
	}
	if res.JSON202 == nil {
		if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
			return "", namespaceAccessError(c.cfg.ActiveOrg.Slug, ns.Namespace)
		}
		return "", apiError(res.HTTPResponse, res.Body,
			res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
	}
	if res.JSON202.Id == nil || *res.JSON202.Id == "" {
		return "", fmt.Errorf("backend returned a 202 with no job id")
	}
	return *res.JSON202.Id, nil
}

// namespaceSyncStats carries the per-namespace result. The
// dashboard's final row renders directly from the SyncJob DTO via
// summarizeForFinishLine; this struct is what flows into the Issues
// diagnostic pass.
type namespaceSyncStats struct {
	pushTotal              int
	pushDone               int
	created                int
	updated                int
	deleted                int
	autotranslateTriggered bool
	autotranslateTotal     int
	autotranslateDone      int
	filesWritten           int
	stillUntranslated      int
	terminalStatus         string
	errorMessage           string
}

func statsFromJob(j *langsync.DtoDTOSyncJob) namespaceSyncStats {
	s := namespaceSyncStats{}
	if j == nil {
		return s
	}
	if j.PushTotal != nil {
		s.pushTotal = *j.PushTotal
	}
	if j.PushDone != nil {
		s.pushDone = *j.PushDone
	}
	if j.CreatedCount != nil {
		s.created = *j.CreatedCount
	}
	if j.UpdatedCount != nil {
		s.updated = *j.UpdatedCount
	}
	if j.DeletedCount != nil {
		s.deleted = *j.DeletedCount
	}
	if j.AutotranslateTriggeredAt != nil && *j.AutotranslateTriggeredAt != "" {
		s.autotranslateTriggered = true
	}
	if j.AutotranslateTotal != nil {
		s.autotranslateTotal = *j.AutotranslateTotal
	}
	if j.AutotranslateDone != nil {
		s.autotranslateDone = *j.AutotranslateDone
	}
	if j.StillUntranslatedCount != nil {
		s.stillUntranslated = *j.StillUntranslatedCount
	}
	if j.Status != nil {
		s.terminalStatus = *j.Status
	}
	if j.ErrorMessage != nil {
		s.errorMessage = *j.ErrorMessage
	}
	return s
}

func writeResultFiles(j *langsync.DtoDTOSyncJob, dir, configPath string, aliases map[string]string) (int, error) {
	if j.ResultPerLanguage == nil {
		return 0, nil
	}
	// Reverse alias map: server code → disk filename code. The
	// forward map (disk → server) is used on the read side; here
	// we need to go the other way to land each language's results
	// in the file the user expects on disk.
	reverse := map[string]string{}
	for diskCode, serverCode := range aliases {
		if serverCode == "" {
			continue
		}
		reverse[serverCode] = diskCode
	}
	written := 0
	for serverCode, marks := range *j.ResultPerLanguage {
		diskCode := serverCode
		if alias, ok := reverse[serverCode]; ok && alias != "" {
			diskCode = alias
		}
		path := filepath.Join(dir, translations.LangFileName(diskCode))
		if err := translations.WriteFlatJSON(path, marks); err != nil {
			return written, fmt.Errorf("write %s: %w", path, err)
		}
		written++
	}
	return written, nil
}

func relPath(configPath, target string) string {
	root := filepath.Dir(configPath)
	if rel, err := filepath.Rel(root, target); err == nil {
		return rel
	}
	return target
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func ptrStr(s string) *string { return &s }
func ptrBool(b bool) *bool    { return &b }

func marksMapPtr(m map[string]string) *map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	return &m
}

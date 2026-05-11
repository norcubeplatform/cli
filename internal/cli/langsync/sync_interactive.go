package langsync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/norcubeplatform/cli/internal/api/langsync"
)

// interactivelyResolveConflicts is the client-side pre-flight for
// `--strategy interactive`. It fetches the namespace's current
// default-language state, compares against the local marks, and
// prompts the user on every conflict (same key, different value).
//
// Returns the marks map with the user's choices baked in. The
// returned map is what gets submitted to the server as a "local"
// strategy job — the backend never knows the user resolved
// interactively, only that this is the authoritative client view.
//
// ErrCancelled means the user aborted; the caller skips the namespace.
func interactivelyResolveConflicts(ctx context.Context, c *langsyncContext, namespace string, local map[string]string, defaultCode string) (map[string]string, error) {
	server, err := fetchDefaultLangValues(ctx, c, namespace, defaultCode)
	if err != nil {
		return nil, err
	}

	type conflict struct {
		mark      string
		localVal  string
		serverVal string
	}
	var conflicts []conflict
	for mark, localVal := range local {
		srv, exists := server[mark]
		if !exists || srv == localVal {
			continue
		}
		conflicts = append(conflicts, conflict{mark: mark, localVal: localVal, serverVal: srv})
	}
	if len(conflicts) == 0 {
		return local, nil
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].mark < conflicts[j].mark })

	const (
		keepLocal     = "keep_local"
		keepServer    = "keep_server"
		keepLocalAll  = "keep_local_all"
		keepServerAll = "keep_server_all"
		skipOne       = "skip"
		abort         = "abort"
	)
	options := []huh.Option[string]{
		huh.NewOption("Keep local (push)", keepLocal),
		huh.NewOption("Keep server (skip update)", keepServer),
		huh.NewOption("Skip this row", skipOne),
		huh.NewOption("Keep local for all remaining", keepLocalAll),
		huh.NewOption("Keep server for all remaining", keepServerAll),
		huh.NewOption("Abort interactive resolution", abort),
	}

	resolved := make(map[string]string, len(local))
	for k, v := range local {
		resolved[k] = v
	}

	autoChoice := ""
	for i, conf := range conflicts {
		choice := autoChoice
		if choice == "" {
			c, err := promptResolveOne(conf.mark, conf.localVal, conf.serverVal, i+1, len(conflicts), options)
			if err != nil {
				return nil, err
			}
			choice = c
		}
		switch choice {
		case keepLocal:
			// resolved[mark] already holds localVal — no change.
		case keepServer, skipOne:
			// Submit the server's value so the backend planner sees
			// no diff on this mark.
			resolved[conf.mark] = conf.serverVal
		case keepLocalAll:
			autoChoice = keepLocal
		case keepServerAll:
			resolved[conf.mark] = conf.serverVal
			autoChoice = keepServer
		case abort:
			return nil, ErrCancelled
		default:
			return nil, fmt.Errorf("internal: unknown interactive choice %q", choice)
		}
	}
	return resolved, nil
}

func promptResolveOne(mark, localVal, serverVal string, index, total int, opts []huh.Option[string]) (string, error) {
	desc := fmt.Sprintf(
		"local:  %s\nserver: %s",
		truncateForPrompt(localVal, 100),
		truncateForPrompt(serverVal, 100),
	)
	var choice string
	title := fmt.Sprintf("[%d/%d] Conflict on %q", index, total, mark)
	// No Height() — only 6 conflict-resolution options; fits in
	// full without the huh viewport-anchored-cursor scroll glitch.
	err := newWizard(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title).
			Description(desc).
			Options(opts...).
			Value(&choice),
	)).Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", ErrCancelled
		}
		return "", err
	}
	return choice, nil
}

func truncateForPrompt(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// fetchDefaultLangValues walks the namespace's paginated /terms
// and returns one map mark→default-language value. Used only by
// interactive mode (opt-in, for power users). Realistic-sized
// namespaces are fine; 10k+ marks would prefer a dedicated
// "give me the default-lang state" endpoint.
func fetchDefaultLangValues(ctx context.Context, c *langsyncContext, namespace, defaultCode string) (map[string]string, error) {
	out := map[string]string{}
	cursor := ""
	for {
		params := &langsync.ListByNamespaceParams{
			Cursor: cursor,
			Limit:  200,
		}
		res, err := c.client.ListByNamespaceWithResponse(ctx, namespace, params)
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
				return nil, namespaceAccessError(c.cfg.ActiveOrg.Slug, namespace)
			}
			return nil, apiError(res.HTTPResponse, res.Body,
				res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
		}
		for _, t := range res.JSON200.List {
			for _, tr := range t.Translations {
				if tr.Lang.Code == defaultCode {
					out[t.Term.Mark] = tr.Value
					break
				}
			}
		}
		if res.JSON200.Cursors.Next == nil || *res.JSON200.Cursors.Next == "" {
			return out, nil
		}
		cursor = *res.JSON200.Cursors.Next
	}
}

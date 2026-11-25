package langsync

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg/translations"
	"github.com/norcubeplatform/cli/internal/clierr"
)

// loadSyncContexts reads the context file of every target that has one,
// before anything is sent, and keeps the descriptions of the source
// file's keys: LangSync refuses a description of a key the request
// doesn't push. Descriptions left out that way are counted in one
// warning line per namespace on w. A context file that can't be read is
// an error, because the person configured it and expects it to be sent.
// The result is keyed by namespace; namespaces without descriptions are
// absent.
func loadSyncContexts(w io.Writer, cfg *projectcfg.File, configPath string, targets []projectcfg.Namespace) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, ns := range targets {
		path := cfg.ResolveContextFile(configPath, ns)
		if path == "" {
			continue
		}
		contexts, err := projectcfg.ReadContexts(path)
		if err != nil {
			return nil, fmt.Errorf("[%s] %w", ns.Namespace, err)
		}
		sourcePath := filepath.Join(cfg.ResolveDir(configPath, ns), translations.LangFileName(ns.DefaultLocalLanguage))
		source, err := translations.ReadFlatJSON(sourcePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("[%s] %w", ns.Namespace, err)
		}
		kept, dropped := sourceContexts(contexts, source)
		if dropped > 0 {
			fmt.Fprintf(w, "[%s] warning: %d %s in %s %s that %s doesn't have; not sent\n",
				ns.Namespace, dropped, plural(dropped, "description", "descriptions"), relPath(configPath, path),
				plural(dropped, "is for a key", "are for keys"), translations.LangFileName(ns.DefaultLocalLanguage))
		}
		if len(kept) > 0 {
			out[ns.Namespace] = kept
		}
	}
	return out, nil
}

// sourceContexts keeps the descriptions whose key is one of the source
// language's keys and counts the others.
func sourceContexts(contexts, source map[string]string) (kept map[string]string, dropped int) {
	kept = make(map[string]string, len(contexts))
	for key, description := range contexts {
		if _, ok := source[key]; !ok {
			dropped++
			continue
		}
		kept[key] = description
	}
	return kept, dropped
}

// syncContextsError words LangSync's refusal of a sync's key
// descriptions (SYNC_CONTEXTS_INVALID). The error keeps the API's code,
// type and data; only the sentence is the CLI's, naming the context
// file to fix.
func syncContextsError(err error, contextFile string, p *langsync.DtoSyncContextsProblem) error {
	var ce *clierr.Error
	if p == nil || !errors.As(err, &ce) {
		return err
	}
	key := ""
	if p.Key != nil {
		key = *p.Key
	}
	var what string
	switch p.Reason {
	case langsync.SyncContextsProblemTooMany:
		what = "there are more descriptions than source keys"
		if p.Max != nil {
			what += fmt.Sprintf(" (the most is %d)", *p.Max)
		}
	case langsync.SyncContextsProblemUnknownKey:
		what = fmt.Sprintf("key %q is not one of the source language's keys", key)
	case langsync.SyncContextsProblemTooLong:
		what = fmt.Sprintf("the description of key %q is too long", key)
		if p.MaxChars != nil {
			what += fmt.Sprintf(" (the most is %d characters)", *p.MaxChars)
		}
	case langsync.SyncContextsProblemNotText:
		what = fmt.Sprintf("the description of key %q is not text", key)
	default:
		what = strings.TrimSpace(string(p.Reason) + " " + key)
	}
	source := "the key descriptions"
	if contextFile != "" {
		source = "the key descriptions from " + contextFile
	}
	ce.Message = fmt.Sprintf("LangSync refused %s: %s", source, what)
	return ce
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

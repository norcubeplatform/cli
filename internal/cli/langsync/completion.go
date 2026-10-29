package langsync

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/completion"
)

// Completion lookup keys (they also separate the cache entries).
const (
	completeNamespaces = "langsync-namespaces"
	completeLanguages  = "langsync-languages"
	completePrompts    = "langsync-prompts"
	completeGlossary   = "langsync-glossary-items"
	completeAPIKeys    = "langsync-api-keys" // #nosec G101 -- a cache key, not a credential
)

// registerCompletions offers namespace names for every --namespace flag
// and `namespaces update|delete`, language codes for `languages
// add|remove`, custom prompt ids for every --prompt / --default-prompt
// and `prompts get|update|delete`, glossary entry ids and API key ids.
func registerCompletions(c *cobra.Command) {
	if c.Flags().Lookup("namespace") != nil {
		_ = c.RegisterFlagCompletionFunc("namespace", completion.Flag(completeNamespaces, namespaceCandidates))
	}
	for _, flag := range []string{"prompt", "default-prompt"} {
		if c.Flags().Lookup(flag) != nil {
			_ = c.RegisterFlagCompletionFunc(flag, completion.Flag(completePrompts, promptCandidates))
		}
	}
	parent := ""
	if c.HasParent() {
		parent = c.Parent().Name()
	}
	switch {
	case parent == "namespaces" && (c.Name() == "update" || c.Name() == "delete"):
		c.ValidArgsFunction = completion.Func(completeNamespaces, 1, namespaceCandidates)
	case parent == "languages" && (c.Name() == "add" || c.Name() == "remove"):
		c.ValidArgsFunction = completion.Func(completeLanguages, 1, languageCandidates)
	case parent == "prompts" && (c.Name() == "get" || c.Name() == "update" || c.Name() == "delete"):
		c.ValidArgsFunction = completion.Func(completePrompts, 1, promptCandidates)
	case parent == "items" && (c.Name() == "get" || c.Name() == "translate" || c.Name() == "delete"):
		c.ValidArgsFunction = completion.Func(completeGlossary, 1, glossaryCandidates)
	case parent == "api-keys" && c.Name() == "revoke":
		c.ValidArgsFunction = completion.Func(completeAPIKeys, 1, apiKeyCandidates)
	}
	for _, sub := range c.Commands() {
		registerCompletions(sub)
	}
}

func namespaceCandidates(cmd *cobra.Command) ([]cobra.Completion, error) {
	c, err := newLangsyncContext(cmd)
	if err != nil {
		return nil, err
	}
	res, err := c.API.ListNamespacesWithResponse(cmd.Context())
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf("status %d", res.StatusCode())
	}
	out := make([]cobra.Completion, 0, len(*res.JSON200))
	for _, ns := range *res.JSON200 {
		if ns.Name != nil && *ns.Name != "" {
			out = append(out, *ns.Name)
		}
	}
	return out, nil
}

func languageCandidates(cmd *cobra.Command) ([]cobra.Completion, error) {
	c, err := newLangsyncContext(cmd)
	if err != nil {
		return nil, err
	}
	langs, err := listAllLanguages(cmd.Context(), c)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(langs))
	for _, l := range langs {
		if l.Code == nil || *l.Code == "" {
			continue
		}
		item := *l.Code
		if l.Name != nil && *l.Name != "" {
			item += "\t" + *l.Name
		}
		out = append(out, item)
	}
	return out, nil
}

func promptCandidates(cmd *cobra.Command) ([]cobra.Completion, error) {
	c, err := newLangsyncContext(cmd)
	if err != nil {
		return nil, err
	}
	prompts, err := listPrompts(cmd, c)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, p.Id+"\t"+p.Name)
	}
	return out, nil
}

func glossaryCandidates(cmd *cobra.Command) ([]cobra.Completion, error) {
	c, err := newLangsyncContext(cmd)
	if err != nil {
		return nil, err
	}
	limit := 100
	res, err := c.API.ListGlossaryItemsWithResponse(cmd.Context(), &langsync.ListGlossaryItemsParams{Limit: &limit})
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf("status %d", res.StatusCode())
	}
	out := make([]cobra.Completion, 0, len(res.JSON200.List))
	for _, i := range res.JSON200.List {
		out = append(out, i.Id+"\t"+i.DefaultLanguageValue)
	}
	return out, nil
}

func apiKeyCandidates(cmd *cobra.Command) ([]cobra.Completion, error) {
	c, err := newLangsyncContext(cmd)
	if err != nil {
		return nil, err
	}
	res, err := c.API.ListAPIKeysWithResponse(cmd.Context())
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf("status %d", res.StatusCode())
	}
	out := make([]cobra.Completion, 0, len(res.JSON200.List))
	for _, k := range res.JSON200.List {
		if apiKeyStatus(k) == "active" {
			out = append(out, k.Id+"\t"+k.Name)
		}
	}
	return out, nil
}

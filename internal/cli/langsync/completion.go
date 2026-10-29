package langsync

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/completion"
)

// Completion lookup keys (they also separate the cache entries).
const (
	completeNamespaces = "langsync-namespaces"
	completeLanguages  = "langsync-languages"
)

// registerCompletions offers namespace names for every --namespace flag
// and `namespaces update|delete`, and language codes for `languages
// add|remove`.
func registerCompletions(c *cobra.Command) {
	if c.Flags().Lookup("namespace") != nil {
		_ = c.RegisterFlagCompletionFunc("namespace", completion.Flag(completeNamespaces, namespaceCandidates))
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

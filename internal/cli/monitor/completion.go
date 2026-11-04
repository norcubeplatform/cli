package monitor

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/completion"
)

// Completion lookup keys (they also separate the cache entries). Each
// lookup is one request of at most 100 items, within completion's
// one-second budget, cached a minute per profile and organization.
const (
	completeMonitors  = "monitor-monitors"
	completeChannels  = "monitor-channels"
	completeIncidents = "monitor-incidents"
)

// registerCompletions offers monitor ids (named, with their status),
// channel ids and open incident ids to the commands and flags that take
// them.
func registerCompletions(root *cobra.Command, d deps) {
	monitors := monitorCandidates(d)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("monitor") != nil {
			_ = c.RegisterFlagCompletionFunc("monitor", completion.Flag(completeMonitors, monitors))
		}
		switch first := firstArg(c.Use); first {
		case "<monitor>":
			c.ValidArgsFunction = completion.Func(completeMonitors, 1, monitors)
		case "[<monitor>...]":
			c.ValidArgsFunction = completion.Func(completeMonitors, 1<<10, monitors)
		case "<channel>":
			c.ValidArgsFunction = completion.Func(completeChannels, 1, channelCandidates(d))
		case "<incident>":
			c.ValidArgsFunction = completion.Func(completeIncidents, 1, incidentCandidates(d))
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// firstArg is the first argument placeholder of a Use line.
func firstArg(use string) string {
	for i := 0; i < len(use); i++ {
		if use[i] == ' ' {
			return use[i+1:]
		}
	}
	return ""
}

func monitorCandidates(d deps) completion.Fetcher {
	return func(cmd *cobra.Command) ([]cobra.Completion, error) {
		c, err := d.client(cmd)
		if err != nil {
			return nil, err
		}
		limit := maxPageSize
		res, err := c.API.ListMonitorsWithResponse(cmd.Context(), &monitor.ListMonitorsParams{Limit: &limit})
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			return nil, fmt.Errorf("status %d", res.StatusCode())
		}
		out := make([]cobra.Completion, 0, len(res.JSON200.List))
		for _, m := range res.JSON200.List {
			out = append(out, fmt.Sprintf("%s\t%s (%s, %s)", m.Id, m.Name, m.Variant, monitorWord(m)))
		}
		return out, nil
	}
}

func channelCandidates(d deps) completion.Fetcher {
	return func(cmd *cobra.Command) ([]cobra.Completion, error) {
		c, err := d.client(cmd)
		if err != nil {
			return nil, err
		}
		limit := maxPageSize
		res, err := c.API.ListAlertChannelsWithResponse(cmd.Context(), &monitor.ListAlertChannelsParams{Limit: &limit})
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			return nil, fmt.Errorf("status %d", res.StatusCode())
		}
		out := make([]cobra.Completion, 0, len(res.JSON200.List))
		for _, ch := range res.JSON200.List {
			out = append(out, fmt.Sprintf("%s\t%s (%s)", ch.Id, ch.Name, ch.Kind))
		}
		return out, nil
	}
}

// incidentCandidates offers the newest incidents, open ones first (the
// API's order).
func incidentCandidates(d deps) completion.Fetcher {
	return func(cmd *cobra.Command) ([]cobra.Completion, error) {
		c, err := d.client(cmd)
		if err != nil {
			return nil, err
		}
		limit := maxPageSize
		res, err := c.API.ListIncidentsWithResponse(cmd.Context(), &monitor.ListIncidentsParams{Limit: &limit})
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			return nil, fmt.Errorf("status %d", res.StatusCode())
		}
		out := make([]cobra.Completion, 0, len(res.JSON200.List))
		for _, i := range res.JSON200.List {
			out = append(out, fmt.Sprintf("%s\t%s, %s: %s", i.Id, i.Status, i.MonitorName, i.Title))
		}
		return out, nil
	}
}

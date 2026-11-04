package monitor

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/clierr"
)

// monitorRef resolves a monitor named on the command line: its id, or its
// exact name (case-insensitive). A name that matches no monitor is a 404
// (exit 3); one that matches several is a usage error naming their ids.
func monitorRef(ctx context.Context, c *monitorContext, ref string) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}
	if ref == "" {
		return uuid.Nil, clierr.Usage(fmt.Errorf("no monitor given"))
	}
	q := ref
	limit := maxPageSize
	var matches []monitor.DtoMonitor
	params := &monitor.ListMonitorsParams{Q: &q, Limit: &limit}
	for {
		res, err := c.API.ListMonitorsWithResponse(ctx, params)
		if err != nil {
			return uuid.Nil, err
		}
		if res.JSON200 == nil {
			return uuid.Nil, apiError(res.HTTPResponse, res.Body)
		}
		for _, m := range res.JSON200.List {
			if strings.EqualFold(m.Name, ref) {
				matches = append(matches, m)
			}
		}
		next := nextCursor(res.JSON200.Cursors)
		if next == "" || len(matches) > 1 {
			break
		}
		params.Cursor = &next
	}
	switch len(matches) {
	case 0:
		return uuid.Nil, &clierr.Error{Code: clierr.CodeNotFound, Message: fmt.Sprintf("no monitor named %q", ref),
			Hint: "List them with `norcube monitor monitors list --search " + ref + "`."}
	case 1:
		return matches[0].Id, nil
	}
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i] = m.Id.String()
	}
	return uuid.Nil, clierr.Usage(fmt.Errorf("%d monitors are named %q: name one by id (%s)", len(matches), ref, strings.Join(ids, ", ")))
}

// idArg parses an id argument (incidents, channels) before any request.
func idArg(what, v string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(v))
	if err != nil {
		return uuid.Nil, clierr.Usage(fmt.Errorf("%q is not a %s id (a UUID such as 7d1c2f7e-6a1f-4f5e-9b8a-1f7c2b3d4e5f)", v, what))
	}
	return id, nil
}

// channelRef resolves an alert channel by id or exact name
// (case-insensitive), the same way monitorRef does.
func channelRef(ctx context.Context, c *monitorContext, ref string) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}
	if ref == "" {
		return uuid.Nil, clierr.Usage(fmt.Errorf("no alert channel given"))
	}
	limit := maxPageSize
	params := &monitor.ListAlertChannelsParams{Limit: &limit}
	var ids []string
	for {
		res, err := c.API.ListAlertChannelsWithResponse(ctx, params)
		if err != nil {
			return uuid.Nil, err
		}
		if res.JSON200 == nil {
			return uuid.Nil, apiError(res.HTTPResponse, res.Body)
		}
		for _, ch := range res.JSON200.List {
			if strings.EqualFold(ch.Name, ref) {
				ids = append(ids, ch.Id.String())
			}
		}
		next := nextCursor(res.JSON200.Cursors)
		if next == "" {
			break
		}
		params.Cursor = &next
	}
	switch len(ids) {
	case 0:
		return uuid.Nil, &clierr.Error{Code: clierr.CodeNotFound, Message: fmt.Sprintf("no alert channel named %q", ref),
			Hint: "List them with `norcube monitor channels list`."}
	case 1:
		return uuid.MustParse(ids[0]), nil
	}
	return uuid.Nil, clierr.Usage(fmt.Errorf("%d alert channels are named %q: name one by id (%s)", len(ids), ref, strings.Join(ids, ", ")))
}

// statusPageID resolves a status page named on the command line: its id,
// which needs no request, or its exact slug.
func statusPageID(ctx context.Context, c *monitorContext, ref string) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}
	p, err := statusPageBySlug(ctx, c, ref)
	if err != nil {
		return uuid.Nil, err
	}
	return p.Id, nil
}

// getStatusPage returns a status page named by its id or exact slug.
func getStatusPage(ctx context.Context, c *monitorContext, ref string) (monitor.DtoStatusPage, error) {
	ref = strings.TrimSpace(ref)
	id, err := uuid.Parse(ref)
	if err != nil {
		return statusPageBySlug(ctx, c, ref)
	}
	res, err := c.API.GetStatusPageWithResponse(ctx, id)
	if err != nil {
		return monitor.DtoStatusPage{}, err
	}
	if res.JSON200 == nil {
		return monitor.DtoStatusPage{}, apiError(res.HTTPResponse, res.Body)
	}
	return *res.JSON200, nil
}

// statusPageBySlug looks through the organization's pages: the API has no
// lookup by slug. Slugs are unique, so at most one matches.
func statusPageBySlug(ctx context.Context, c *monitorContext, slug string) (monitor.DtoStatusPage, error) {
	if slug == "" {
		return monitor.DtoStatusPage{}, clierr.Usage(fmt.Errorf("no status page given"))
	}
	limit := maxPageSize
	params := &monitor.ListStatusPagesParams{Limit: &limit}
	for {
		res, err := c.API.ListStatusPagesWithResponse(ctx, params)
		if err != nil {
			return monitor.DtoStatusPage{}, err
		}
		if res.JSON200 == nil {
			return monitor.DtoStatusPage{}, apiError(res.HTTPResponse, res.Body)
		}
		for _, p := range res.JSON200.List {
			if p.Slug == slug {
				return p, nil
			}
		}
		next := nextCursor(res.JSON200.Cursors)
		if next == "" {
			break
		}
		params.Cursor = &next
	}
	return monitor.DtoStatusPage{}, &clierr.Error{Code: clierr.CodeNotFound, Message: fmt.Sprintf("no status page with the slug %q", slug),
		Hint: "List them with `norcube monitor status-pages list`."}
}

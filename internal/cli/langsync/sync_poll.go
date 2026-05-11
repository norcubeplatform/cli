package langsync

import (
	"context"
	"fmt"
	"time"

	"github.com/norcubeplatform/cli/internal/api/langsync"
)

// pollSyncJobDashboard polls one sync job and reports state to the
// shared dashboard row. Returns the last-seen job state — useful
// for the post-run Issues block which inspects e.g. the failures
// array.
func pollSyncJobDashboard(ctx context.Context, c *langsyncContext, d *dashboard, namespace, jobID string, opts syncOptions) (*langsync.DtoDTOSyncJob, error) {
	pollEvery := opts.pollEvery
	if pollEvery <= 0 {
		pollEvery = 1 * time.Second
	}
	timeout := opts.waitTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)

	var lastJob *langsync.DtoDTOSyncJob
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()

	for {
		job, err := fetchSyncJob(ctx, c, namespace, jobID)
		if err != nil {
			return lastJob, err
		}
		lastJob = job

		phase := derefStr(job.Status)
		current, total, detail := phaseDisplay(phase, job)
		d.Update(namespace, phase, current, total, detail)

		if isTerminal(phase) {
			if phase == "failed" {
				return job, fmt.Errorf("%s", derefStr(job.ErrorMessage))
			}
			return job, nil
		}
		if time.Now().After(deadline) {
			// Don't fail outright — the server is still running.
			// Just stop polling and let the caller (parallel sync)
			// flag the row as "client poll timed out" via stats.
			return lastJob, nil
		}
		select {
		case <-ctx.Done():
			return lastJob, ctx.Err()
		case <-tick.C:
		}
	}
}

func fetchSyncJob(ctx context.Context, c *langsyncContext, namespace, jobID string) (*langsync.DtoDTOSyncJob, error) {
	res, err := c.client.GetNamespacesNamespaceNameSyncJobIdWithResponse(ctx, namespace, jobID)
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
	return res.JSON200, nil
}

// phaseDisplay maps a job's current state into the dashboard's
// (current, total, detail) triple. Different phases have different
// progress models — pushing tracks ops; autotranslating tracks
// cells; planning/finalizing have no counter at all.
func phaseDisplay(phase string, job *langsync.DtoDTOSyncJob) (current, total int, detail string) {
	switch phase {
	case "pushing":
		return derefInt(job.PushDone), derefInt(job.PushTotal), ""
	case "autotranslating":
		// During autotranslating the progress bar still benefits
		// from a denominator (it's a bar — fraction is the point),
		// but the *label* should not invite "but I only created N
		// marks" confusion. So we say "translating cells" and let
		// the bar do the talking.
		t := derefInt(job.AutotranslateTotal)
		done := derefInt(job.AutotranslateDone)
		if t > 0 && done > t {
			done = t
		}
		return done, t, "translating cells"
	case "planning":
		return 0, 0, "computing diff"
	case "finalizing":
		return 0, 0, "reading per-language state"
	case "pending":
		return 0, 0, "waiting for a worker to claim the job"
	case "completed", "planned":
		return 0, 0, ""
	}
	return 0, 0, ""
}

// summarizeForFinishLine is the one-liner that lands in the
// dashboard's "done" state per row.
func summarizeForFinishLine(job *langsync.DtoDTOSyncJob, filesWritten int) string {
	if job == nil {
		return "no result"
	}
	parts := []string{}
	if v := derefInt(job.CreatedCount); v > 0 {
		parts = append(parts, fmt.Sprintf("%d created", v))
	}
	if v := derefInt(job.UpdatedCount); v > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", v))
	}
	if v := derefInt(job.DeletedCount); v > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", v))
	}
	// "N cells translated" + "M still empty" is the user-facing
	// pair. Don't show the namespace-wide-untranslated denominator
	// (the old "/N" suffix) — it's an intermediate that doesn't
	// add information and only invites "but I only created 4
	// marks, why is the total 19?" confusion. The numbers that
	// matter are what the LLM filled and what's still empty.
	if v := derefInt(job.AutotranslateDone); v > 0 {
		parts = append(parts, fmt.Sprintf("%d cells translated", v))
	}
	if v := derefInt(job.StillUntranslatedCount); v > 0 {
		parts = append(parts, fmt.Sprintf("%d still empty", v))
	}
	if filesWritten > 0 {
		parts = append(parts, fmt.Sprintf("%d files written", filesWritten))
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return joinComma(parts)
}

func isTerminal(s string) bool {
	switch s {
	case "completed", "planned", "failed", "cancelled":
		return true
	}
	return false
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

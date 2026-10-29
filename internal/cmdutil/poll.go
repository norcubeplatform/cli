package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// WaitFlags are the flags of commands that can wait for a server-side job
// (a bulk check, a name generation) to finish.
type WaitFlags struct {
	Wait    bool
	Timeout time.Duration
}

// AddWaitFlags registers --wait and --wait-timeout.
func AddWaitFlags(cmd *cobra.Command, wf *WaitFlags, what string, defaultTimeout time.Duration) {
	cmd.Flags().BoolVar(&wf.Wait, "wait", false, fmt.Sprintf("Wait until the %s finishes (Ctrl-C stops waiting, not the %s)", what, what))
	cmd.Flags().DurationVar(&wf.Timeout, "wait-timeout", defaultTimeout, "Give up waiting after this long (the job keeps running on the server)")
}

// PollOptions controls Poll. Zero values pick sensible defaults.
type PollOptions struct {
	// Interval is the first delay between checks (default 1s).
	Interval time.Duration
	// MaxInterval caps the growing delay (default 15s).
	MaxInterval time.Duration
	// Timeout ends the wait with ErrWaitTimeout (0 = no limit).
	Timeout time.Duration
	// Sleep waits between checks; tests replace it. It must return early
	// with ctx.Err() when ctx ends.
	Sleep func(ctx context.Context, d time.Duration) error
}

// ErrWaitTimeout is returned by Poll when Timeout passes before check
// reports done. The job is still running on the server.
var ErrWaitTimeout error = &clierr.Error{
	Code:    clierr.CodeTimeout,
	Message: "stopped waiting: the job is still running on the server",
	Hint:    "Check on it later, or wait longer with --wait-timeout.",
}

// Poll calls check until it reports done, an error, the context ends
// (Ctrl-C), or the timeout passes. The delay between calls starts at
// Interval and grows by half each time up to MaxInterval, so a long job
// doesn't flood the API while a short one finishes promptly.
func Poll(ctx context.Context, opts PollOptions, check func(ctx context.Context) (done bool, err error)) error {
	interval := opts.Interval
	if interval <= 0 {
		interval = time.Second
	}
	maxInterval := opts.MaxInterval
	if maxInterval <= 0 {
		maxInterval = 15 * time.Second
	}
	if maxInterval < interval {
		maxInterval = interval
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = SleepContext
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	for {
		done, err := check(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
				return ErrWaitTimeout
			}
			return err
		}
		if done {
			return nil
		}
		if err := sleep(ctx, interval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return ErrWaitTimeout
			}
			return err
		}
		interval += interval / 2
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}

// SleepContext waits for d or until ctx ends, whichever is first.
func SleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// JobFailed is the error of a command that waited for a job (--wait)
// which ended unsuccessfully: exit 1 with code job_failed, so a script
// can tell "the backup failed" from "the CLI failed". job is the job as
// last seen, included in the JSON error as details.
func JobFailed(message string, job any) error {
	return &clierr.Error{Code: clierr.CodeJobFailed, Message: message, Details: job}
}

package langsync

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// dashboard renders the live state of one or more sync jobs in
// parallel. Each namespace owns one row; the rows redraw in place
// via ANSI cursor-up + clear-line on a TTY, or fall back to plain
// line-per-phase-change logging on a non-TTY (CI).
//
// Lifecycle: NewDashboard → spawn worker goroutines that call
// dashboard.Row(name).Update(...) → dashboard.Close() blocks for a
// final render and stops the ticker. Output after Close() is plain
// stdout (issue block, summary, etc.).
type dashboard struct {
	w     io.Writer
	tty   bool
	color bool

	mu     sync.Mutex
	slots  []*slotState
	byName map[string]*slotState

	// linesDrawn tracks how many rows we last rendered, so the
	// next redraw can move the cursor up by exactly that many
	// before re-drawing. Used only in TTY mode.
	linesDrawn int

	// labelWidth is the right-padded width of the namespace
	// column. Computed once from the longest namespace name at
	// construction; rows wider than this get clipped.
	labelWidth int
	phaseWidth int

	closed atomic.Bool
	stop   chan struct{}
	done   chan struct{}
}

type slotState struct {
	namespace string

	// Display fields, all string for cheap concatenation. The
	// renderer reads these under the dashboard mutex.
	phase      string
	current    int
	total      int
	detail     string
	final      bool
	failed     bool
	startedAt  time.Time
	finishedAt time.Time

	// loggedPhase is the phase we last emitted a line for in
	// non-TTY mode, so we don't spam the log with one entry per
	// poll tick.
	loggedPhase string
}

// newDashboard sets up the renderer. namespaces[] gives the row
// order (config-file order, deterministic across runs).
func newDashboard(w io.Writer, namespaces []string) *dashboard {
	d := &dashboard{
		w:      w,
		byName: make(map[string]*slotState, len(namespaces)),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if f, ok := w.(*os.File); ok {
		d.tty = term.IsTerminal(int(f.Fd()))
	}
	if d.tty && os.Getenv("NO_COLOR") == "" {
		d.color = true
	}
	for _, ns := range namespaces {
		s := &slotState{namespace: ns, phase: "queued", startedAt: time.Now()}
		d.slots = append(d.slots, s)
		d.byName[ns] = s
		if l := len(ns); l > d.labelWidth {
			d.labelWidth = l
		}
	}
	// Phase column width — sized to the widest expected status
	// string so columns line up cleanly across rows mid-flight.
	d.phaseWidth = len("autotranslating") // 15
	return d
}

// Start kicks off the render ticker. Safe to call from any goroutine;
// the renderer runs until Close() is called.
func (d *dashboard) Start() {
	if !d.tty {
		// On non-TTY we render on every Update call inline. No
		// ticker needed.
		close(d.done)
		return
	}
	go d.renderLoop()
}

func (d *dashboard) renderLoop() {
	defer close(d.done)
	// Initial render so the user sees N rows immediately, before
	// any goroutine has updated state.
	d.render()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			d.render() // one last pass with final state
			return
		case <-t.C:
			d.render()
		}
	}
}

// Update mutates the named slot's display state. Fields left at
// their zero value are not touched (use the constants for "leave
// alone" semantics where it matters).
func (d *dashboard) Update(namespace string, phase string, current, total int, detail string) {
	d.mu.Lock()
	s, ok := d.byName[namespace]
	if !ok {
		d.mu.Unlock()
		return
	}
	if phase != "" {
		s.phase = phase
	}
	s.current = current
	s.total = total
	s.detail = detail
	d.mu.Unlock()

	if !d.tty {
		d.maybeLogNonTTY(s)
	}
}

// Complete marks a slot as terminally done with a summary line. The
// final state stays on screen for the rest of the run.
func (d *dashboard) Complete(namespace, finalDetail string) {
	d.mu.Lock()
	s, ok := d.byName[namespace]
	if !ok {
		d.mu.Unlock()
		return
	}
	s.phase = "done"
	s.detail = finalDetail
	s.final = true
	s.finishedAt = time.Now()
	s.current = 0
	s.total = 0
	d.mu.Unlock()

	if !d.tty {
		d.maybeLogNonTTY(s)
	}
}

// Fail is like Complete but flags the row as an error.
func (d *dashboard) Fail(namespace string, err error) {
	d.mu.Lock()
	s, ok := d.byName[namespace]
	if !ok {
		d.mu.Unlock()
		return
	}
	s.phase = "failed"
	s.detail = err.Error()
	s.final = true
	s.failed = true
	s.finishedAt = time.Now()
	s.current = 0
	s.total = 0
	d.mu.Unlock()

	if !d.tty {
		d.maybeLogNonTTY(s)
	}
}

// Close flushes one final render, stops the ticker, and positions
// the cursor below the dashboard so subsequent output flows
// naturally. Idempotent — safe to call from a defer.
func (d *dashboard) Close() {
	if d.closed.Swap(true) {
		return
	}
	if d.tty {
		close(d.stop)
		<-d.done
		// One final blank line so the issue block / summary that
		// follows doesn't run flush against the dashboard.
		fmt.Fprintln(d.w)
		return
	}
	// non-TTY: nothing buffered to flush.
	<-d.done
}

// maybeLogNonTTY emits a "[<ns>] <phase>: <detail>" line if the
// slot's phase changed since the last log. Keeps the log readable
// in CI without spamming one line per poll tick.
func (d *dashboard) maybeLogNonTTY(s *slotState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.phase == s.loggedPhase {
		return
	}
	s.loggedPhase = s.phase
	line := fmt.Sprintf("[%s] %s", s.namespace, s.phase)
	if s.detail != "" {
		line += ": " + s.detail
	}
	fmt.Fprintln(d.w, line)
}

// render redraws every row under the dashboard mutex. TTY only.
// Uses ANSI cursor-up to overwrite the previous frame in place.
func (d *dashboard) render() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.linesDrawn > 0 {
		// Move up to the first row, then ClearEOS to wipe
		// everything we drew last frame in one shot. \033[J is
		// "clear from cursor to end of screen" — simpler than
		// per-line clears and avoids partial leftovers.
		fmt.Fprintf(d.w, "\033[%dA\r\033[J", d.linesDrawn)
	}
	for _, s := range d.slots {
		fmt.Fprintln(d.w, d.renderRow(s))
	}
	d.linesDrawn = len(d.slots)
}

func (d *dashboard) renderRow(s *slotState) string {
	bullet := "▸"
	name := padRight(s.namespace, d.labelWidth)
	if d.color {
		name = lipgloss.NewStyle().Bold(true).Render(name)
	}
	phase := padRight(s.phase, d.phaseWidth)
	if d.color {
		phase = d.colorPhase(s, phase)
	}

	body := ""
	if s.total > 0 && !s.final {
		body = d.renderBar(s.current, s.total, 18) + "  " + fmt.Sprintf("%d/%d", s.current, s.total)
	} else if !s.final && s.current > 0 {
		body = fmt.Sprintf("%d", s.current)
	}
	if s.detail != "" {
		if body != "" {
			body += "  "
		}
		body += s.detail
	}
	if s.final {
		dur := time.Since(s.startedAt)
		if !s.finishedAt.IsZero() {
			dur = s.finishedAt.Sub(s.startedAt)
		}
		body += "  " + d.dim("("+formatDuration(dur)+")")
	}
	return fmt.Sprintf("%s %s  %s  %s", bullet, name, phase, body)
}

var (
	dashOKStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	dashErrStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	dashActiveStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	dashIdleStyle    = lipgloss.NewStyle().Faint(true)
	dashBarFill      = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	dashBarTrack     = lipgloss.NewStyle().Faint(true)
	dashSummaryStyle = lipgloss.NewStyle().Bold(true)
)

func (d *dashboard) colorPhase(s *slotState, label string) string {
	switch {
	case s.failed:
		return dashErrStyle.Render(label)
	case s.final:
		return dashOKStyle.Render(label)
	case s.phase == "queued" || s.phase == "pending":
		return dashIdleStyle.Render(label)
	}
	return dashActiveStyle.Render(label)
}

func (d *dashboard) renderBar(current, total, width int) string {
	if total <= 0 {
		return strings.Repeat(" ", width)
	}
	if current < 0 {
		current = 0
	}
	if current > total {
		current = total
	}
	filled := current * width / total
	if filled > width {
		filled = width
	}
	fill := strings.Repeat("█", filled)
	track := strings.Repeat("░", width-filled)
	if d.color {
		return dashBarFill.Render(fill) + dashBarTrack.Render(track)
	}
	return fill + track
}

func (d *dashboard) dim(s string) string {
	if d.color {
		return dashIdleStyle.Render(s)
	}
	return s
}

// PrintHeading writes a styled section heading below the dashboard.
// Used for the post-run "Issues" / "Summary" blocks.
func (d *dashboard) PrintHeading(text string) {
	if d.color {
		fmt.Fprintln(d.w, dashSummaryStyle.Render("▸ "+text))
	} else {
		fmt.Fprintln(d.w, "▸ "+text)
	}
}

// PrintNote writes a non-styled indented note line below the dashboard.
// Used for the Issues block entries.
func (d *dashboard) PrintNote(text string) {
	if d.color {
		fmt.Fprintln(d.w, dashIdleStyle.Render("  "+text))
		return
	}
	fmt.Fprintln(d.w, "  "+text)
}

// PrintLine writes a plain line below the dashboard. For per-namespace
// summary rows where the namespace name should still be bold.
func (d *dashboard) PrintRow(namespace, text string) {
	if d.color {
		fmt.Fprintf(d.w, "%s  %s\n", dashSummaryStyle.Render(namespace), text)
		return
	}
	fmt.Fprintf(d.w, "%s  %s\n", namespace, text)
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return d.String()
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

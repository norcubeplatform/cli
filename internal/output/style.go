package output

import (
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Style controls how a Table is rendered. Header bold, status colors, and
// dim placeholders are all conditional on isInteractive(w) and the
// NO_COLOR environment variable — pipes and CI logs get plain text.
type Style struct {
	// StatusColumn is the 0-based column index whose values should be
	// recognised as success/failed/running etc. and colored. Negative
	// means no column is treated as status.
	StatusColumn int
}

// NoStyle is the zero value Style — no status column. Spelled out for
// readability at call sites.
var NoStyle = Style{StatusColumn: -1}

// styling collects flags for the current writer, so we make the
// terminal-capability + NO_COLOR decision once per Print call.
type styling struct {
	enabled bool
}

func styleFor(w io.Writer) styling {
	// NO_COLOR (https://no-color.org) is the de facto standard: any
	// non-empty value disables color. Honor it before checking isatty so
	// users can force-disable even in interactive terminals.
	if os.Getenv("NO_COLOR") != "" {
		return styling{enabled: false}
	}
	return styling{enabled: isInteractive(w)}
}

var (
	headerStyle = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)

	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))  // green
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // yellow
	failureStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))  // red
	infoStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // bright blue
)

// styleHeader applies bold to a header cell when styling is enabled. The
// returned string includes ANSI escapes, so callers must avoid measuring
// its length for column alignment — feed the raw header to tabwriter, then
// style after. (For now we cheat and rely on lipgloss's width preservation:
// ANSI escapes don't consume terminal cells, and tabwriter aligns by tab
// count, not visible width — so styled headers fit existing columns.)
func (s styling) styleHeader(v string) string {
	if !s.enabled {
		return v
	}
	return headerStyle.Render(v)
}

// styleStatus colors a status value (e.g. "success", "failed") if it
// matches a known severity bucket. Unknown statuses are left alone.
func (s styling) styleStatus(v string) string {
	if !s.enabled {
		return v
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "success", "ok", "succeeded", "passed", "active", "ready", "completed", "done":
		return successStyle.Render(v)
	case "failed", "error", "errored", "denied", "rejected":
		return failureStyle.Render(v)
	case "running", "pending", "queued", "in_progress", "starting", "scheduled":
		return runningStyle.Render(v)
	case "partial", "warning", "deprecated":
		return infoStyle.Render(v)
	case "inactive", "disabled", "off", "paused", "stopped":
		return dimStyle.Render(v)
	}
	return v
}

// styleDim renders placeholder values (—, (none), -) in a dim style so
// the eye can skip them. Only triggered for an exact match against the
// known set; real data is left alone.
func (s styling) styleDim(v string) string {
	if !s.enabled {
		return v
	}
	switch strings.TrimSpace(v) {
	case "—", "-", "", "(none)", "(empty)", "null":
		return dimStyle.Render(v)
	}
	return v
}

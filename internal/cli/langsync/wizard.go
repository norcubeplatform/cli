package langsync

import (
	"github.com/charmbracelet/huh"
)

// newWizard wraps huh.NewForm with the langsync package's standard
// styling: ThemeCharm (polished default), help footer, and error
// rendering. Every interactive prompt in this package should
// instantiate forms via this helper so the visual stays consistent
// across init / sync / pull / namespace create / org create.
//
// Pass each prompt as its own huh.NewGroup — huh treats each group
// as a wizard step (one screen at a time, with a "1/N" footer
// rendered automatically). Putting multiple fields in the same
// group makes them render simultaneously, which produces multiple
// blinking cursors and a worse UX. The one-field-per-group rule is
// what separates this implementation from the previous build.
func newWizard(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).
		WithTheme(huh.ThemeCharm()).
		WithShowHelp(true).
		WithShowErrors(true)
}

// stepNote builds a NewNote whose title doubles as a wizard step
// indicator ("Step N of M — <title>"). Workaround for huh #510 — a
// Select with Filtering(true) hides its own title once the user
// starts typing, so a NewNote pinned above the Select keeps the
// step context visible.
func stepNote(step, total int, title, description string) *huh.Note {
	heading := title
	if total > 1 {
		heading = formatStepHeading(step, total, title)
	}
	n := huh.NewNote().Title(heading)
	if description != "" {
		n = n.Description(description)
	}
	return n
}

// formatStepHeading is split out for unit-test friendliness and
// reuse — kept tiny so callers don't have to import fmt just to
// build a step label.
func formatStepHeading(step, total int, title string) string {
	switch {
	case step <= 0 || total <= 0:
		return title
	case total == 1:
		return title
	}
	// "Step 2 of 3 — Default language" reads cleaner than the more
	// compact "[2/3]" you'd see in package managers; this is closer
	// to the create-next-app / npm init style.
	return formatStep(step, total) + " — " + title
}

// formatStep is the bottom-level format helper. Pulled out so the
// "Step N of M" string format is in exactly one place; lets a
// future change to e.g. "[2/3]" stay a one-line edit.
func formatStep(step, total int) string {
	return "Step " + itoa(step) + " of " + itoa(total)
}

// itoa is a fmt-free int→string for very small numbers. Wizard
// step counts are always single digits in practice; this saves the
// fmt import in this otherwise-tiny file.
func itoa(n int) string {
	if n < 0 {
		return "?"
	}
	if n == 0 {
		return "0"
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

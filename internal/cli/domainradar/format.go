package domainradar

import (
	"fmt"
	"strings"

	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// availability is the table cell for a check result: what a person
// wants to know first.
func availability(c domainradar.DtoDomainCheck) string {
	if e := cmdutil.Deref(c.Error); e != "" {
		return "error"
	}
	switch {
	case c.Available == nil:
		return ""
	case *c.Available:
		return "available"
	default:
		return "registered"
	}
}

// checkRow is the shared table row of a domain check (single checks, bulk
// results, the live stream).
func checkRow(c domainradar.DtoDomainCheck) []string {
	cached := ""
	if cmdutil.Deref(c.CacheHit) {
		cached = "cached"
	}
	return []string{
		cmdutil.Deref(c.Domain),
		availability(c),
		cmdutil.Deref(c.Method),
		dateOnly(cmdutil.Deref(c.ExpiryDate)),
		cmdutil.Deref(c.Registrar),
		cached,
	}
}

var checkHeaders = []string{"DOMAIN", "AVAILABILITY", "METHOD", "EXPIRES", "REGISTRAR", "CACHE"}

func checkTable(items []domainradar.DtoDomainCheck) output.Table[domainradar.DtoDomainCheck] {
	return output.Table[domainradar.DtoDomainCheck]{
		Headers:   checkHeaders,
		MaxWidths: []int{0, 0, 0, 0, 32, 0},
		Style:     output.Style{StatusColumn: 1},
		Rows:      checkRow,
		Items:     items,
	}
}

// dateOnly trims a timestamp to its date (registries report expiry
// dates with an arbitrary time of day).
func dateOnly(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return s
}

// progress renders "completed/total (pct%)".
func progress(done, total int) string {
	if total <= 0 {
		return fmt.Sprintf("%d", done)
	}
	return fmt.Sprintf("%d/%d (%d%%)", done, total, done*100/total)
}

func onOff(b *bool) string {
	if cmdutil.Deref(b) {
		return "on"
	}
	return "off"
}

func joinList(p *[]string) string {
	if p == nil {
		return ""
	}
	return strings.Join(*p, ", ")
}

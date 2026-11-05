package monitor

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// Status words are Norcube Monitor's own vocabulary (the service README's
// status model): an HTTP monitor is Up or Down, a certificate Valid,
// Expiring or Expired, a domain Registered or Expiring. JSON keeps the
// API's codes (status, condition); only tables and text show words.
var conditionWords = map[string]map[string]string{
	"http": {
		"ok": "Up", "slow_response": "Slow", "config_error": "Can't check", "internal": "Unknown",
	},
	"ssl": {
		"ok": "Valid", "cert_expiring": "Expiring", "cert_expired": "Expired",
		"cert_invalid": "Invalid", "cert_hostname": "Invalid", "cert_not_yet_valid": "Invalid",
		"timeout": "Unreachable", "dns": "Unreachable", "connect": "Unreachable", "tls": "Unreachable",
		"blocked_destination": "Unreachable", "internal": "Unknown",
	},
	"domain": {
		"ok": "Registered", "domain_expiring": "Expiring", "domain_expiry_unknown": "Expiry unknown",
		"domain_expired": "Expired", "domain_not_registered": "Not registered",
		"domain_lookup_failed": "Unknown", "internal": "Unknown",
	},
}

// statusWord names a monitor's status in its type's words. HTTP problems
// other than the ones listed are Down.
func statusWord(monitorType, status, condition string) string {
	switch status {
	case "paused":
		return "Paused"
	case "pending":
		return "Pending"
	}
	if w, ok := conditionWords[monitorType][condition]; ok {
		return w
	}
	switch status {
	case "ok":
		return conditionWords[monitorType]["ok"]
	case "critical":
		if monitorType == "http" {
			return "Down"
		}
		return "Critical"
	case "warning":
		return "Warning"
	}
	return capitalize(status)
}

// aspectWord names a watched aspect's status (an HTTP monitor's
// certificate or domain watch) in that aspect's words.
func aspectWord(aspect, status, condition string) string {
	kind := map[string]string{"certificate": "ssl", "domain": "domain"}[aspect]
	if status == "pending" {
		return "Pending"
	}
	if w, ok := conditionWords[kind][condition]; ok {
		return w
	}
	return capitalize(status)
}

// severityWord names a check result's severity for a monitor type.
func resultWord(monitorType, severity, condition string) string {
	if severity == "inconclusive" {
		return "Unknown"
	}
	return statusWord(monitorType, severity, condition)
}

// stamp renders an API time inside a longer cell (a timeline line), in
// local time to the minute; whole-cell times use output.WhenString.
func stamp(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

// plural is "1 check", "2 checks".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// percent renders an uptime share ("99.95%"); no data is the placeholder,
// never 100%.
func percent(p *float32) string {
	if p == nil {
		return "—"
	}
	return strconv.FormatFloat(float64(*p), 'f', -1, 32) + "%"
}

func ms(p *int) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%d ms", *p)
}

func intOr(p *int) string {
	if p == nil {
		return "—"
	}
	return strconv.Itoa(*p)
}

func strOr(p *string) string {
	if p == nil || *p == "" {
		return "—"
	}
	return *p
}

// orNone joins lines for one multi-line field.
func orNone(lines []string) string {
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// duration renders seconds as "3d 4h", "2h 5m", "4m 10s".
func duration(seconds int) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// interval renders a check interval ("1m", "12h").
func interval(seconds int) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", seconds)
}

func joinInts(v []int) string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, ", ")
}

// ---------- monitors ----------

var monitorHeaders = []string{"ID", "NAME", "TYPE", "STATUS", "TARGET", "UPTIME 24H", "LAST CHECK"}

func monitorWord(m monitor.DtoMonitor) string {
	cond := ""
	if m.Condition != nil {
		cond = m.Condition.Code
	}
	return statusWord(string(m.Type), string(m.Status), cond)
}

func monitorRow(m monitor.DtoMonitor) []string {
	uptime, last := "—", "—"
	if m.Last24h != nil {
		uptime = percent(m.Last24h.UptimePercent)
	}
	if m.LastCheck != nil {
		last = output.WhenString(m.LastCheck.At)
	}
	return []string{m.Id.String(), m.Name, string(m.Variant), monitorWord(m), m.Target, uptime, last}
}

func monitorTable(items []monitor.DtoMonitor) output.Table[monitor.DtoMonitor] {
	return output.Table[monitor.DtoMonitor]{
		Headers:   monitorHeaders,
		MaxWidths: []int{0, 32, 0, 0, 48, 0, 0},
		Style:     output.Style{StatusColumn: 3},
		Rows:      monitorRow,
		Items:     items,
	}
}

func monitorObject(m monitor.DtoMonitor) output.Object {
	f := []output.Field{
		{Name: "ID", Value: m.Id.String()},
		{Name: "Name", Value: m.Name},
		{Name: "Type", Value: string(m.Variant)},
		{Name: "Target", Value: m.Target},
		{Name: "Status", Value: monitorWord(m)},
		{Name: "Since", Value: output.WhenString(m.StatusSince)},
	}
	if m.Condition != nil && m.Condition.Message != nil {
		f = append(f, output.Field{Name: "Reason", Value: *m.Condition.Message})
	}
	if m.Condition != nil && m.Condition.Facts.DaysRemaining != nil {
		f = append(f, output.Field{Name: "Days left", Value: strconv.Itoa(*m.Condition.Facts.DaysRemaining)})
	}
	for _, a := range m.Aspects {
		v := aspectWord(string(a.Aspect), string(a.Status), cmdutil.Deref(a.Condition))
		if a.DaysRemaining != nil {
			v += fmt.Sprintf(" (%d days left)", *a.DaysRemaining)
		}
		f = append(f, output.Field{Name: capitalize(string(a.Aspect)) + " of " + a.Subject, Value: v})
	}
	f = append(f, output.Field{Name: "Interval", Value: interval(m.IntervalSeconds)})
	if m.Last24h != nil {
		f = append(f, output.Field{Name: "Uptime 24h", Value: percent(m.Last24h.UptimePercent)},
			output.Field{Name: "Response p95", Value: ms(m.Last24h.P95Ms)})
	}
	last, next := "—", output.WhenString(cmdutil.Deref(m.NextCheckAt))
	if m.LastCheck != nil {
		last = output.WhenString(m.LastCheck.At)
	}
	regions := make([]string, len(m.Regions))
	for i, r := range m.Regions {
		regions[i] = r.Name
	}
	f = append(f,
		output.Field{Name: "Last check", Value: last},
		output.Field{Name: "Next check", Value: next},
		output.Field{Name: "Regions", Value: strings.Join(regions, ", ")},
	)
	f = append(f, settingsFields(m)...)
	if m.ParentResource != nil {
		f = append(f, output.Field{Name: "Managed by", Value: *m.ParentResource})
	}
	if m.Description != "" {
		f = append(f, output.Field{Name: "Description", Value: m.Description})
	}
	f = append(f, output.Field{Name: "Created", Value: output.WhenString(m.CreatedAt)})
	// -o text prints the list's columns: the fields above depend on the type.
	return output.Object{Value: m, Fields: f, Text: monitorRow(m)}
}

// settingsFields are a monitor's check settings for its detail view.
// Secret header values never reach the CLI (the API withholds them).
func settingsFields(m monitor.DtoMonitor) []output.Field {
	var f []output.Field
	switch {
	case m.Http != nil:
		h := m.Http
		f = append(f,
			output.Field{Name: "Request", Value: h.Method + " " + h.Url},
			output.Field{Name: "Expected status", Value: strings.Join(h.ExpectedStatus, ", ")},
			output.Field{Name: "Timeout", Value: fmt.Sprintf("%d ms", h.TimeoutMs)},
		)
		if h.SlowThresholdMs != nil {
			f = append(f, output.Field{Name: "Slow above", Value: ms(h.SlowThresholdMs)})
		}
		if h.Keyword != nil {
			mode := cmdutil.DerefOr(h.KeywordMode, "present")
			f = append(f, output.Field{Name: "Keyword", Value: fmt.Sprintf("%q must be %s", *h.Keyword, mode)})
		}
		for _, hd := range h.Headers {
			v := cmdutil.Deref(hd.Value)
			if hd.Secret {
				v = "(secret)"
			}
			f = append(f, output.Field{Name: "Header " + hd.Name, Value: v})
		}
		redirects := "not followed"
		if h.FollowRedirects {
			redirects = fmt.Sprintf("followed, at most %d", h.MaxRedirects)
		}
		f = append(f, output.Field{Name: "Redirects", Value: redirects},
			output.Field{Name: "Verify TLS", Value: yesNo(h.VerifyTls)})
	case m.Ssl != nil:
		f = append(f, output.Field{Name: "Host", Value: fmt.Sprintf("%s:%d", m.Ssl.Host, m.Ssl.Port)})
		if m.Ssl.ServerName != nil {
			f = append(f, output.Field{Name: "Server name", Value: *m.Ssl.ServerName})
		}
		f = append(f, output.Field{Name: "Alert at days", Value: joinInts(m.Ssl.ExpiryThresholdsDays)})
	case m.Domain != nil:
		f = append(f, output.Field{Name: "Domain", Value: m.Domain.Domain},
			output.Field{Name: "Alert at days", Value: joinInts(m.Domain.ExpiryThresholdsDays)})
	}
	return f
}

// ---------- check results ----------

var resultHeaders = []string{"ID", "CHECKED", "RESULT", "CONDITION", "STATUS CODE", "TIME", "REGION"}

func resultRow(monitorType string) func(r monitor.DtoCheckResult) []string {
	return func(r monitor.DtoCheckResult) []string {
		return []string{
			strconv.Itoa(r.Id),
			output.WhenStringSeconds(r.CheckedAt),
			resultWord(monitorType, string(r.Severity), r.Condition),
			r.Condition,
			intOr(r.StatusCode),
			fmt.Sprintf("%d ms", r.Timings.TotalMs),
			r.Region,
		}
	}
}

func resultObject(monitorType string, r monitor.DtoCheckResult) output.Object {
	row := resultRow(monitorType)(r)
	f := []output.Field{
		{Name: "Result", Value: row[2]},
		{Name: "Condition", Value: r.Condition},
		{Name: "Checked", Value: row[1]},
		{Name: "Status code", Value: row[4]},
		{Name: "Total time", Value: row[5]},
		{Name: "Region", Value: r.Region},
	}
	if r.ErrorMessage != nil {
		f = append(f, output.Field{Name: "Message", Value: *r.ErrorMessage})
	}
	if c := r.Certificate; c != nil {
		f = append(f, output.Field{Name: "Certificate", Value: fmt.Sprintf("%s, expires %s (%d days)", c.Subject, output.WhenString(c.NotAfter), c.DaysRemaining)})
	}
	if dr := r.Domain; dr != nil {
		f = append(f, output.Field{Name: "Domain expires", Value: output.WhenString(cmdutil.Deref(dr.ExpiresAt))},
			output.Field{Name: "Registrar", Value: strOr(dr.Registrar)})
	}
	f = append(f, output.Field{Name: "Result ID", Value: row[0]})
	return output.Object{Value: r, Fields: f, Text: row}
}

// ---------- incidents ----------

var incidentHeaders = []string{"ID", "STATUS", "SEVERITY", "MONITOR", "TITLE", "OPENED", "DURATION", "ACK"}

func incidentRow(i monitor.DtoIncident) []string {
	ack := "no"
	if i.AcknowledgedAt != nil {
		ack = "yes"
	}
	return []string{
		i.Id.String(), string(i.Status), string(i.Severity), i.MonitorName, i.Title,
		output.WhenString(i.OpenedAt), duration(i.DurationSeconds), ack,
	}
}

func incidentTable(items []monitor.DtoIncident) output.Table[monitor.DtoIncident] {
	return output.Table[monitor.DtoIncident]{
		Headers:   incidentHeaders,
		MaxWidths: []int{0, 0, 0, 28, 48, 0, 0, 0},
		Style:     output.Style{StatusColumn: 2},
		Rows:      incidentRow,
		Items:     items,
	}
}

func incidentObject(i monitor.DtoIncidentDetail) output.Object {
	ack := "no"
	if i.AcknowledgedAt != nil {
		ack = output.WhenString(*i.AcknowledgedAt)
		if i.AcknowledgedByName != nil {
			ack += " by " + *i.AcknowledgedByName
		}
	}
	f := []output.Field{
		{Name: "ID", Value: i.Id.String()},
		{Name: "Title", Value: i.Title},
		{Name: "Status", Value: string(i.Status)},
		{Name: "Severity", Value: fmt.Sprintf("%s (peak %s)", i.Severity, i.PeakSeverity)},
		{Name: "Monitor", Value: fmt.Sprintf("%s (%s, %s)", i.MonitorName, i.MonitorVariant, i.MonitorTarget)},
		{Name: "Aspect", Value: string(i.Aspect)},
		{Name: "Summary", Value: i.Summary},
		{Name: "Opened", Value: output.WhenString(i.OpenedAt)},
		{Name: "Resolved", Value: output.WhenString(cmdutil.Deref(i.ResolvedAt))},
		{Name: "Duration", Value: duration(i.DurationSeconds)},
		{Name: "Acknowledged", Value: ack},
	}
	timeline := make([]string, 0, len(i.Timeline))
	for _, e := range i.Timeline {
		timeline = append(timeline, stamp(e.At)+"  "+string(e.Type))
	}
	alerts := make([]string, 0, len(i.Alerts))
	for _, a := range i.Alerts {
		to := cmdutil.DerefOr(a.ChannelName, "member emails")
		alerts = append(alerts, fmt.Sprintf("%s  %s to %s: %s (%s)", stamp(a.QueuedAt), a.Event, to, a.Status, plural(a.Attempts, "attempt")))
	}
	f = append(f, output.Field{Name: "Timeline", Value: orNone(timeline)}, output.Field{Name: "Alerts", Value: orNone(alerts)})
	row := incidentRow(monitor.DtoIncident{
		Id: i.Id, Status: monitor.DtoIncidentStatus(i.Status), Severity: monitor.DtoIncidentSeverity(i.Severity),
		MonitorName: i.MonitorName, Title: i.Title, OpenedAt: i.OpenedAt, DurationSeconds: i.DurationSeconds, AcknowledgedAt: i.AcknowledgedAt,
	})
	return output.Object{Value: i, Fields: f, Text: row}
}

// ---------- alert channels ----------

var channelHeaders = []string{"ID", "NAME", "KIND", "DESTINATION", "DEFAULT", "ENABLED", "VERIFIED"}

// destination is where a channel delivers, as the API shows it (never a
// credential: Slack and Discord URLs are hints).
func destination(c monitor.DtoAlertChannel) string {
	s := c.Settings
	switch {
	case cmdutil.Deref(s.Members):
		return "organization members"
	case s.Address != nil:
		return *s.Address
	case s.Phone != nil:
		return *s.Phone
	case s.Hint != nil:
		return *s.Hint
	case s.Url != nil:
		return *s.Url
	}
	return "—"
}

func channelRow(c monitor.DtoAlertChannel) []string {
	return []string{c.Id.String(), c.Name, string(c.Kind), destination(c), yesNo(c.IsDefault), yesNo(c.Enabled), yesNo(c.Verified)}
}

func channelTable(items []monitor.DtoAlertChannel) output.Table[monitor.DtoAlertChannel] {
	return output.Table[monitor.DtoAlertChannel]{
		Headers:   channelHeaders,
		MaxWidths: []int{0, 28, 0, 40, 0, 0, 0},
		Style:     output.NoStyle,
		Rows:      channelRow,
		Items:     items,
	}
}

func channelObject(c monitor.DtoAlertChannel) output.Object {
	row := channelRow(c)
	f := make([]output.Field, 0, len(row)+2)
	names := []string{"ID", "Name", "Kind", "Destination", "Default", "Enabled", "Verified"}
	for i, n := range names {
		f = append(f, output.Field{Name: n, Value: row[i]})
	}
	f = append(f, output.Field{Name: "Created", Value: output.WhenString(c.CreatedAt)},
		output.Field{Name: "Updated", Value: output.WhenString(c.UpdatedAt)})
	return output.Object{Value: c, Fields: f}
}

// withoutSecret drops the one-time signing secret from a channel the API
// returned with one, so it never reaches stdout.
func withoutSecret(c monitor.DtoAlertChannelWithSecret) monitor.DtoAlertChannel {
	return monitor.DtoAlertChannel{
		CreatedAt: c.CreatedAt, Enabled: c.Enabled, Id: c.Id, IsDefault: c.IsDefault,
		Kind: monitor.DtoAlertChannelKind(c.Kind), Name: c.Name, Settings: c.Settings,
		UpdatedAt: c.UpdatedAt, Verified: c.Verified,
	}
}

// ---------- status pages ----------

func statusPageState(p monitor.DtoStatusPage) string {
	if p.Published {
		return "published"
	}
	return "draft"
}

func statusPageRow(p monitor.DtoStatusPage) []string {
	return []string{p.Id.String(), p.Slug, p.Name, statusPageState(p), p.Url, output.WhenString(p.UpdatedAt)}
}

func statusPageTable(items []monitor.DtoStatusPage) output.Table[monitor.DtoStatusPage] {
	return output.Table[monitor.DtoStatusPage]{
		Headers:   []string{"ID", "SLUG", "NAME", "STATE", "URL", "UPDATED"},
		MaxWidths: []int{0, 0, 32, 0, 0, 0},
		Rows:      statusPageRow,
		Items:     items,
	}
}

func statusPageObject(p monitor.DtoStatusPage) output.Object {
	s := p.Settings
	return output.Object{Value: p, Text: statusPageRow(p), Fields: []output.Field{
		{Name: "ID", Value: p.Id.String()},
		{Name: "Slug", Value: p.Slug},
		{Name: "Name", Value: p.Name},
		{Name: "Description", Value: p.Description},
		{Name: "URL", Value: p.Url},
		{Name: "State", Value: statusPageState(p)},
		{Name: "Published", Value: output.WhenString(cmdutil.Deref(p.PublishedAt))},
		{Name: "Time zone", Value: s.TimeZone},
		{Name: "Uptime days", Value: strconv.Itoa(int(s.UptimeDays))},
		{Name: "History days", Value: strconv.Itoa(s.HistoryDays)},
		{Name: "Uptime bars", Value: yesNo(s.ShowUptimeBars)},
		{Name: "Uptime percent", Value: yesNo(s.ShowUptimePercent)},
		{Name: "Response time", Value: yesNo(s.ShowResponseTime)},
		{Name: "Search indexing", Value: yesNo(s.SearchIndexing)},
		{Name: "Automatic posts", Value: yesNo(cmdutil.Deref(s.AutoPost))},
		{Name: "Created", Value: output.WhenString(p.CreatedAt)},
		{Name: "Updated", Value: output.WhenString(p.UpdatedAt)},
	}}
}

// statusPageComponentLine is one component of a layout: its public name,
// where its status comes from, and its id.
func statusPageComponentLine(x monitor.DtoStatusPageComponent) string {
	source := string(cmdutil.Deref(x.ManualStatus)) + " (manual)"
	if x.MonitorId != nil {
		aspects := make([]string, 0, len(cmdutil.Deref(x.Aspects)))
		for _, a := range cmdutil.Deref(x.Aspects) {
			aspects = append(aspects, string(a))
		}
		source = "monitor " + x.MonitorId.String() + " (" + strings.Join(aspects, ", ") + ")"
	}
	id := ""
	if x.Id != nil {
		id = x.Id.String() + "  "
	}
	return id + x.Name + ": " + source
}

func statusPageComponentLines(items []monitor.DtoStatusPageComponent) string {
	lines := make([]string, 0, len(items))
	for _, x := range items {
		lines = append(lines, statusPageComponentLine(x))
	}
	return orNone(lines)
}

// statusPageLayoutObject shows a layout as its rows; JSON is the document
// `layout set` takes. -o text prints the counts.
func statusPageLayoutObject(l monitor.DtoStatusPageLayout) output.Object {
	count := len(l.Components)
	fields := []output.Field{{Name: "Components", Value: statusPageComponentLines(l.Components)}}
	for _, g := range l.Groups {
		count += len(g.Components)
		fields = append(fields, output.Field{Name: "Group " + g.Name, Value: statusPageComponentLines(g.Components)})
	}
	return output.Object{Value: l, Fields: fields, Text: []string{strconv.Itoa(count), strconv.Itoa(len(l.Groups))}}
}

// ---------- subscribers ----------

// subscriberComponents says what a subscriber follows: an empty list means
// every component of the page.
func subscriberComponents(s monitor.DtoStatusPageSubscriber) string {
	if len(s.ComponentIds) == 0 {
		return "all"
	}
	return strconv.Itoa(len(s.ComponentIds))
}

func subscriberTable(items []monitor.DtoStatusPageSubscriber) output.Table[monitor.DtoStatusPageSubscriber] {
	return output.Table[monitor.DtoStatusPageSubscriber]{
		Headers: []string{"ID", "EMAIL", "STATUS", "LOCALE", "COMPONENTS", "CREATED", "CONFIRMED"},
		Rows: func(s monitor.DtoStatusPageSubscriber) []string {
			return []string{
				s.Id.String(), s.Email, string(s.Status), string(s.Locale), subscriberComponents(s),
				output.WhenString(s.CreatedAt), output.WhenString(cmdutil.Deref(s.ConfirmedAt)),
			}
		},
		Items: items,
	}
}

// ---------- posted incidents ----------

func postRow(i monitor.DtoStatusPageIncident) []string {
	return []string{
		i.Id.String(), string(i.Status), string(i.Impact), i.Title,
		output.WhenString(i.StartedAt), output.WhenString(cmdutil.Deref(i.ResolvedAt)), strconv.Itoa(len(i.Updates)),
	}
}

func postTable(items []monitor.DtoStatusPageIncident) output.Table[monitor.DtoStatusPageIncident] {
	return output.Table[monitor.DtoStatusPageIncident]{
		Headers:   []string{"ID", "STATUS", "IMPACT", "TITLE", "STARTED", "RESOLVED", "UPDATES"},
		MaxWidths: []int{0, 0, 0, 48, 0, 0, 0},
		Rows:      postRow,
		Items:     items,
	}
}

func postObject(p monitor.DtoStatusPage, i monitor.DtoStatusPageIncident) output.Object {
	address := "not public: the page isn't published"
	if p.Published {
		address = postURL(p, i.Id)
	}
	components := make([]string, 0, len(i.Components))
	for _, x := range i.Components {
		components = append(components, x.ComponentId.String()+"  "+string(x.Status))
	}
	updates := make([]string, 0, len(i.Updates))
	for _, u := range i.Updates {
		updates = append(updates, stamp(u.CreatedAt)+"  "+string(u.Status)+": "+u.Body)
	}
	fields := []output.Field{
		{Name: "ID", Value: i.Id.String()},
		{Name: "Title", Value: i.Title},
		{Name: "Status", Value: string(i.Status)},
		{Name: "Impact", Value: string(i.Impact)},
		{Name: "Started", Value: output.WhenString(i.StartedAt)},
		{Name: "Resolved", Value: output.WhenString(cmdutil.Deref(i.ResolvedAt))},
		{Name: "Address", Value: address},
		{Name: "Components", Value: orNone(components)},
		{Name: "Monitor incidents", Value: orNone(i.MonitorIncidentIds)},
		{Name: "Updates", Value: orNone(updates)},
	}
	if i.AutoPosted {
		who := "yes, a person has taken it over"
		if i.AutoManaged {
			who = "yes, and the system still runs it"
		}
		fields = append(fields, output.Field{Name: "Posted automatically", Value: who})
	}
	return output.Object{Value: i, Text: postRow(i), Fields: fields}
}

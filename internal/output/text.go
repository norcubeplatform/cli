package output

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// FormatText is tab-separated values without headers, one record per line,
// for cut, awk and shell loops.
const FormatText = "text"

// placeholder is what table cells show for "no value"; text output leaves
// those fields empty instead.
const placeholder = "—"

// Object is a single resource. JSON and YAML encode Value; a table shows
// Fields as aligned name/value lines; text prints the field values on one
// tab-separated line, in Fields order.
type Object struct {
	Value  any
	Fields []Field
}

// Field is one line of an Object's table view.
type Field struct {
	Name  string
	Value string
}

func (o Object) exposeItems() any { return o.Value }

func (o Object) writeTable(w io.Writer) error {
	// A multi-line value (a prompt, a translation) continues on the next
	// lines, aligned under the first.
	type line struct{ label, value string }
	var lines []line
	for _, f := range o.Fields {
		for i, v := range strings.Split(strings.ReplaceAll(f.Value, "\r\n", "\n"), "\n") {
			l := line{value: v}
			if i == 0 {
				l.label = f.Name + ":"
			}
			lines = append(lines, l)
		}
	}
	return Table[line]{
		Rows:  func(l line) []string { return []string{l.label, l.value} },
		Items: lines,
		Style: NoStyle,
	}.writeTable(w)
}

func (o Object) writeText(w io.Writer) error {
	cells := make([]string, len(o.Fields))
	for i, f := range o.Fields {
		cells[i] = textCell(f.Value)
	}
	return writeLine(w, cells)
}

func (t Table[T]) writeText(w io.Writer) error {
	for _, item := range t.Items {
		cells := t.Rows(item)
		for i, c := range cells {
			cells[i] = textCell(c)
		}
		if err := writeLine(w, cells); err != nil {
			return err
		}
	}
	return nil
}

func textCell(s string) string {
	if s == placeholder || s == "-" {
		return ""
	}
	if t, ok := textTime(s); ok {
		return t
	}
	// A tab or newline inside a value would break the record.
	return strings.NewReplacer("\t", " ", "\n", " ").Replace(s)
}

func writeLine(w io.Writer, cells []string) error {
	_, err := io.WriteString(w, strings.Join(cells, "\t")+"\n")
	return err
}

func printText(w io.Writer, value any) error {
	if t, ok := value.(interface{ writeText(io.Writer) error }); ok {
		return t.writeText(w)
	}
	g, err := toGeneric(payload(value))
	if err != nil {
		return err
	}
	return genericText(w, g)
}

// Generic is a value of JSON shape (what --query returns): maps, slices,
// strings, float64s, bools and nil. Tables and text are derived from its
// shape since no column layout exists for it.
type Generic struct{ V any }

func (g Generic) exposeItems() any { return g.V }

func (g Generic) writeTable(w io.Writer) error { return genericTable(w, g.V) }

func (g Generic) writeText(w io.Writer) error { return genericText(w, g.V) }

// toGeneric converts v to its JSON shape.
func toGeneric(v any) (any, error) {
	if g, ok := v.(Generic); ok {
		return g.V, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case json.Number:
		return x.String(), true
	}
	return "", false
}

// cell renders any JSON value for one table or text cell: scalars as is,
// nested values as compact JSON.
func cell(v any) string {
	if s, ok := scalar(v); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// genericText follows `aws --output text`: a scalar on its own line, a list
// of scalars on one tab-separated line, a list of objects or lists one
// line per element, an object as its values in key order.
func genericText(w io.Writer, v any) error {
	if s, ok := scalar(v); ok {
		if v == nil {
			return nil
		}
		return writeLine(w, []string{textCell(s)})
	}
	switch x := v.(type) {
	case []any:
		allScalar := true
		for _, e := range x {
			if _, ok := scalar(e); !ok {
				allScalar = false
				break
			}
		}
		if allScalar {
			if len(x) == 0 {
				return nil
			}
			cells := make([]string, len(x))
			for i, e := range x {
				cells[i] = textCell(cell(e))
			}
			return writeLine(w, cells)
		}
		for _, e := range x {
			if err := genericLine(w, e); err != nil {
				return err
			}
		}
		return nil
	default:
		return genericLine(w, v)
	}
}

func genericLine(w io.Writer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := sortedKeys(x)
		cells := make([]string, len(keys))
		for i, k := range keys {
			cells[i] = textCell(cell(x[k]))
		}
		return writeLine(w, cells)
	case []any:
		cells := make([]string, len(x))
		for i, e := range x {
			cells[i] = textCell(cell(e))
		}
		return writeLine(w, cells)
	}
	return writeLine(w, []string{textCell(cell(v))})
}

// genericTable renders a JSON-shaped value: a list of objects as a table
// whose columns are the objects' keys, an object as KEY/VALUE lines, and
// anything else like text.
func genericTable(w io.Writer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := sortedKeys(x)
		fields := make([]Field, len(keys))
		for i, k := range keys {
			fields[i] = Field{Name: k, Value: cell(x[k])}
		}
		return Object{Fields: fields}.writeTable(w)
	case []any:
		var cols []string
		seen := map[string]bool{}
		for _, e := range x {
			m, ok := e.(map[string]any)
			if !ok {
				return genericText(w, v)
			}
			for k := range m {
				if !seen[k] {
					seen[k] = true
					cols = append(cols, k)
				}
			}
		}
		if len(cols) == 0 {
			return genericText(w, v)
		}
		sort.Strings(cols)
		headers := make([]string, len(cols))
		for i, c := range cols {
			headers[i] = strings.ToUpper(c)
		}
		return Table[any]{
			Headers: headers,
			Rows: func(e any) []string {
				m := e.(map[string]any)
				row := make([]string, len(cols))
				for i, c := range cols {
					row[i] = cell(m[c])
				}
				return row
			},
			Items: x,
			Style: NoStyle,
		}.writeTable(w)
	}
	return genericText(w, v)
}

// encodeYAML writes v as YAML with the same keys and values -o json
// prints: v goes through its JSON shape first, because the generated API
// types carry json tags only (yaml would lowercase the Go field names).
func encodeYAML(w io.Writer, v any) error {
	g, err := toGeneric(v)
	if err != nil {
		return err
	}
	return yaml.NewEncoder(w).Encode(wholeNumbers(g))
}

// wholeNumbers turns integral float64s (how JSON numbers decode) back into
// integers, so YAML prints 1048576, not 1.048576e+06.
func wholeNumbers(v any) any {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x)
		}
	case []any:
		for i := range x {
			x[i] = wholeNumbers(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = wholeNumbers(x[k])
		}
	}
	return v
}

// errUnknownFormat is the error for a format Print doesn't know.
func errUnknownFormat(format string) error {
	return fmt.Errorf("unknown output format %q (want table | json | yaml | text)", format)
}

// Done is the result of an action. A table shows Message, the sentence a
// person expects ("Deleted namespace "web"."); JSON and YAML encode
// Value, the affected resource; text prints Fields on one line, or Value
// in its generic text form when there are no Fields.
type Done struct {
	Message string
	Value   any
	Fields  []Field
}

func (d Done) exposeItems() any { return d.Value }

func (d Done) writeTable(w io.Writer) error {
	if d.Message == "" {
		return nil
	}
	_, err := io.WriteString(w, d.Message+"\n")
	return err
}

func (d Done) writeText(w io.Writer) error {
	if len(d.Fields) > 0 {
		return Object{Fields: d.Fields}.writeText(w)
	}
	g, err := toGeneric(d.Value)
	if err != nil {
		return err
	}
	return genericText(w, g)
}

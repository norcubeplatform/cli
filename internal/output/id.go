package output

import (
	"errors"
	"fmt"
	"io"
)

// FormatID prints only the result's id, one per line for a list, so a script
// can capture what it just created: id=$(... -o id). A result with nothing in
// it prints nothing.
const FormatID = "id"

// ErrNoID says the result has no id to print.
var ErrNoID = errors.New("this result has no id: use -o json or -o text")

func printID(w io.Writer, value any) error {
	g, err := toGeneric(payload(value))
	if err != nil {
		return err
	}
	ids, err := idsOf(g)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := fmt.Fprintln(w, id); err != nil {
			return err
		}
	}
	return nil
}

// idsOf reads the ids of a value of JSON shape. A plain value is taken as an
// id, which is what --query leaves behind.
func idsOf(v any) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		id, ok := scalar(x["id"])
		if !ok || id == "" {
			return nil, ErrNoID
		}
		return []string{id}, nil
	case []any:
		var out []string
		for _, item := range x {
			ids, err := idsOf(item)
			if err != nil {
				return nil, err
			}
			out = append(out, ids...)
		}
		return out, nil
	}
	if s, ok := scalar(v); ok {
		return []string{s}, nil
	}
	return nil, ErrNoID
}

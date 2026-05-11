// Package translations reads and writes language files for the sync
// command. Currently only the flat-JSON format used by every
// Norcube-internal project is implemented (`{"key": "value", ...}`).
//
// All readers return map[string]string with values stripped of nothing
// — empty strings are preserved as empty translations. All writers emit
// a stable key order (sorted, case-sensitive) so diffs from sync are
// minimal and reviewable.
package translations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FlatJSONExt is the file extension we read/write for the flat-json
// format. Files are named "<language-code>.json" (e.g. "cs.json",
// "en-US.json") inside the namespace directory.
const FlatJSONExt = ".json"

// LangFileName returns the canonical filename for the given language
// code inside a flat-json directory. The code is *not* normalized —
// what the backend gives us is what we write to disk, so the file
// names match the backend's view exactly.
func LangFileName(code string) string {
	return code + FlatJSONExt
}

// LangCodeFromFileName is the inverse of LangFileName. Returns the
// code stripped of its extension, or empty when the filename does not
// look like a flat-json translation file.
func LangCodeFromFileName(name string) string {
	if !strings.HasSuffix(name, FlatJSONExt) {
		return ""
	}
	return strings.TrimSuffix(name, FlatJSONExt)
}

// ReadFlatJSON loads a flat-JSON translation file. Missing files
// return (nil, os.ErrNotExist) so callers can decide whether absence
// is an error or an empty-state.
//
// The JSON must decode to map[string]string — nested objects, numbers,
// and arrays are rejected (these would silently corrupt a sync round
// trip otherwise).
func ReadFlatJSON(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	// json.Unmarshal into map[string]string already rejects nested
	// objects and arrays with a typed error, but it accepts numbers /
	// bools by coercing — guard against that with json.Decoder so the
	// caller gets a precise message instead of a silent stringification.
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields() // no effect on maps, but harmless and future-proof
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode %s: %w (expected flat object {\"key\":\"value\", ...})", path, err)
	}
	return out, nil
}

// WriteFlatJSON serializes data to path with sorted keys, two-space
// indentation, and a trailing newline. The write is atomic (temp +
// rename) so a crash mid-write can never leave a half-written file.
func WriteFlatJSON(path string, data map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf strings.Builder
	buf.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n  ")
		kb, err := json.Marshal(k)
		if err != nil {
			return fmt.Errorf("encode key %q: %w", k, err)
		}
		buf.Write(kb)
		buf.WriteString(": ")
		vb, err := json.Marshal(data[k])
		if err != nil {
			return fmt.Errorf("encode value for %q: %w", k, err)
		}
		buf.Write(vb)
	}
	if len(keys) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(buf.String()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ListLangsInDir scans dir for files matching the flat-json convention
// and returns the discovered language codes (basename minus ".json"),
// sorted, plus the absolute paths in matching order.
//
// Subdirectories and dotfiles are skipped. A missing dir is not an
// error — returns (nil, nil) so init/sync can treat "directory not
// yet created" the same as "no languages yet".
func ListLangsInDir(dir string) (codes []string, paths []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	type entry struct{ code, path string }
	var hits []entry
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		code := LangCodeFromFileName(e.Name())
		if code == "" {
			continue
		}
		hits = append(hits, entry{code: code, path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].code < hits[j].code })
	for _, h := range hits {
		codes = append(codes, h.code)
		paths = append(paths, h.path)
	}
	return codes, paths, nil
}

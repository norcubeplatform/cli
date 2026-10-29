package langsync

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// maxImportFile caps the CSV the CLI reads into memory for an upload.
const maxImportFile = 64 << 20

// Column uses of a CSV import (besides a language code, for a
// translation column).
const (
	columnMark    = "mark"
	columnContext = "context"
	columnIgnore  = "ignore"
)

// Header names that map to the mark and context columns without a
// --column flag.
var (
	markHeaders    = []string{"mark", "key", "term", "source"}
	contextHeaders = []string{"context", "description", "comment", "note", "notes"}
)

// importColumn is how one CSV column is imported.
type importColumn struct {
	Header string `json:"header" yaml:"header"`
	// Use is mark, context, ignore or translation.
	Use      string `json:"use" yaml:"use"`
	Language string `json:"language,omitempty" yaml:"language,omitempty"`

	languageID int
}

// importResult is what `terms import -o json` prints.
type importResult struct {
	Namespace string         `json:"namespace" yaml:"namespace"`
	File      string         `json:"file" yaml:"file"`
	ImportID  string         `json:"importId,omitempty" yaml:"importId,omitempty"`
	Rows      int            `json:"rows" yaml:"rows"`
	Status    string         `json:"status" yaml:"status"`
	Columns   []importColumn `json:"columns" yaml:"columns"`
}

func newImportCmd() *cobra.Command {
	var (
		namespaceName, prompt    string
		columns                  []string
		overwrite, autoTranslate bool
		dryRun                   bool
	)
	cmd := &cobra.Command{
		Use:   "import <file.csv>",
		Short: "Import terms and translations from a CSV file",
		Long: `Imports a CSV file with a header row into a namespace: one column holds
the marks (source strings), the others context or translations.

Columns are matched by their header: mark, key, term or source is the
mark; context, description, comment or note is the context; a language
code or name attached to the namespace (en, cs, "Czech") holds that
language's translations. Map any other header with
--column "<header>=<mark|context|ignore|language code>"; the command
refuses to guess.

Existing terms with the same mark are skipped unless --overwrite is set.
With --auto-translate, AI fills in the languages the file leaves empty.
The import finishes on the server after the command returns.

--dry-run shows how each column would be imported, without uploading.`,
		Example: `  norcube langsync terms import strings.csv --namespace web
  norcube langsync terms import export.csv -n web --column "String ID=mark" --column "Notes=ignore"
  norcube langsync terms import strings.csv -n web --overwrite --auto-translate --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			path := args[0]
			if !strings.EqualFold(filepath.Ext(path), ".csv") {
				return nil, clierr.Usage(fmt.Errorf("%s: only .csv files can be imported", path))
			}
			promptID, err := optionalPrompt(cmd.Flags().Changed("prompt"), prompt)
			if err != nil {
				return nil, err
			}
			overrides, err := parseColumnFlags(columns)
			if err != nil {
				return nil, err
			}
			data, header, rows, err := readImportFile(path)
			if err != nil {
				return nil, err
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Import terms into which namespace?")
			if err != nil {
				return nil, cancelledOK(cmd, err)
			}
			conns, err := listConnections(cmd.Context(), c, ns)
			if err != nil {
				return nil, err
			}
			plan, err := planColumns(header, overrides, conns)
			if err != nil {
				return nil, err
			}
			result := importResult{Namespace: ns, File: filepath.Base(path), Rows: rows, Status: "planned", Columns: plan}
			if dryRun {
				return output.Done{Message: describeImport(result, true), Value: result}, nil
			}

			pre, err := uploadImport(cmd, c, ns, filepath.Base(path), data)
			if err != nil {
				return nil, err
			}
			result.ImportID, result.Rows = pre.ImportId, pre.TotalRows
			if pre.Details.Csv != nil && pre.Details.Csv.ColumnNames != nil && len(*pre.Details.Csv.ColumnNames) != len(plan) {
				return nil, fmt.Errorf("the server read %d columns in %s, the CLI %d; check the file's quoting", len(*pre.Details.Csv.ColumnNames), path, len(plan))
			}
			setup := make([]langsync.SharedTermImportCSVColumnSetup, len(plan))
			for i, col := range plan {
				setup[i].Type = langsync.ConstantsTermImportCSVColumnSetupType(col.Use)
				if col.Use == "translation" {
					setup[i].Translation = &langsync.SharedTermImportCSVColumnSetupTranslation{LanguageId: col.languageID}
				}
			}
			res, err := c.API.ImportFinishWithResponse(cmd.Context(), ns, pre.ImportId, langsync.TermImportFinishRequest{
				Setup: langsync.SharedTermImportSetup{
					Csv:                    &langsync.SharedTermImportCSVSetup{ColumnSetup: setup},
					OverwriteExisting:      overwrite,
					TranslateAutomatically: autoTranslate,
					CustomPromptId:         promptID,
				},
			})
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, namespaceAPIError(c, ns, res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			result.Status = string(res.JSON200.Status)
			return output.Done{Message: describeImport(result, false), Value: result, Fields: []output.Field{{Value: result.ImportID}, {Value: result.Status}}}, nil
		}),
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringArrayVar(&columns, "column", nil, `Map a CSV header: "<header>=<mark|context|ignore|language code>" (repeatable)`)
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Replace existing terms that have the same mark")
	cmd.Flags().BoolVar(&autoTranslate, "auto-translate", false, "AI-translate the languages the file leaves empty")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt id for --auto-translate instead of the namespace's default")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show how each column would be imported, without uploading")
	return cmd
}

// parseColumnFlags reads --column "header=use" values, keyed by the
// header in lower case.
func parseColumnFlags(values []string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for _, v := range values {
		header, use, ok := strings.Cut(v, "=")
		header, use = strings.TrimSpace(header), strings.TrimSpace(use)
		if !ok || header == "" || use == "" {
			return nil, clierr.Usage(fmt.Errorf(`--column %q: want "<header>=<mark|context|ignore|language code>"`, v))
		}
		key := strings.ToLower(header)
		if _, dup := out[key]; dup {
			return nil, clierr.Usage(fmt.Errorf("--column maps %q twice", header))
		}
		out[key] = use
	}
	return out, nil
}

// readImportFile reads the CSV, its header row and how many rows follow
// it, so a file the server would reject fails before anything is
// uploaded.
func readImportFile(path string) ([]byte, []string, int, error) {
	f, err := os.Open(path) // #nosec G304 -- the user names the file
	if err != nil {
		return nil, nil, 0, clierr.Usage(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxImportFile+1))
	if err != nil {
		return nil, nil, 0, err
	}
	if len(data) > maxImportFile {
		return nil, nil, 0, clierr.Usage(fmt.Errorf("%s is larger than %d MiB; split it into several imports", path, maxImportFile>>20))
	}
	r := csv.NewReader(bytes.NewReader(data))
	header, err := r.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, 0, clierr.Usage(fmt.Errorf("%s is empty: it needs a header row", path))
		}
		return nil, nil, 0, clierr.Usage(fmt.Errorf("%s: %w", path, err))
	}
	rows := 0
	for ; ; rows++ {
		if _, err := r.Read(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, 0, clierr.Usage(fmt.Errorf("%s: %w", path, err))
		}
	}
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff"))
	}
	return data, header, rows, nil
}

// planColumns decides how each column is imported: --column first, then
// the header names. It fails on a header it can't place, a missing or
// repeated mark column, and a language mapped twice.
func planColumns(header []string, overrides map[string]string, conns []langsync.DtoDTOLanguageConnection) ([]importColumn, error) {
	findLang := func(s string) (langsync.DtoDTOLanguageConnection, bool) {
		for _, conn := range conns {
			if strings.EqualFold(cmdutil.Deref(conn.LanguageCode), s) || strings.EqualFold(cmdutil.Deref(conn.LanguageName), s) {
				return conn, conn.LanguageId != nil
			}
		}
		return langsync.DtoDTOLanguageConnection{}, false
	}
	plan := make([]importColumn, len(header))
	var unknown []string
	used := map[string]bool{}
	for i, h := range header {
		col := importColumn{Header: h}
		use, explicit := overrides[strings.ToLower(h)]
		if explicit {
			delete(overrides, strings.ToLower(h))
		} else {
			use = h
		}
		switch lower := strings.ToLower(use); {
		case lower == columnIgnore && explicit:
			col.Use = columnIgnore
		case lower == columnMark && explicit, !explicit && slices.Contains(markHeaders, lower):
			col.Use = columnMark
		case lower == columnContext && explicit, !explicit && slices.Contains(contextHeaders, lower):
			col.Use = columnContext
		default:
			conn, ok := findLang(use)
			if !ok {
				if explicit {
					return nil, clierr.Usage(fmt.Errorf("--column %q: %q is not mark, context, ignore or a language attached to the namespace", h, use))
				}
				unknown = append(unknown, h)
				continue
			}
			col.Use, col.Language, col.languageID = "translation", cmdutil.Deref(conn.LanguageCode), *conn.LanguageId
			if used["lang:"+col.Language] {
				return nil, clierr.Usage(fmt.Errorf("two columns hold the %s translations; map one with --column \"<header>=ignore\"", col.Language))
			}
			used["lang:"+col.Language] = true
		}
		if col.Use == columnMark {
			if used[columnMark] {
				return nil, clierr.Usage(errors.New("two columns hold the mark; map one with --column \"<header>=ignore\""))
			}
			used[columnMark] = true
		}
		plan[i] = col
	}
	if len(overrides) > 0 {
		var missing []string
		for h := range overrides {
			missing = append(missing, h)
		}
		slices.Sort(missing)
		return nil, clierr.Usage(fmt.Errorf("--column names headers the file doesn't have: %s", strings.Join(missing, ", ")))
	}
	if len(unknown) > 0 {
		return nil, clierr.Usage(fmt.Errorf("can't tell how to import the column(s) %s: map each with --column \"<header>=<mark|context|ignore|language code>\"", quoteAll(unknown)))
	}
	if !used[columnMark] {
		return nil, clierr.Usage(errors.New("no column holds the mark (source string): name its header mark, key or term, or map it with --column \"<header>=mark\""))
	}
	return plan, nil
}

func quoteAll(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

// uploadImport sends the file to the precheck endpoint.
func uploadImport(cmd *cobra.Command, c *langsyncContext, ns, name string, data []byte) (*langsync.TermImportPrecheckResponse, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	h.Set("Content-Type", "text/csv")
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	res, err := c.API.CreateImportWithBodyWithResponse(cmd.Context(), ns, mw.FormDataContentType(), &body)
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, namespaceAPIError(c, ns, res.HTTPResponse, res.Body,
			res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
	}
	return res.JSON200, nil
}

func describeImport(r importResult, dryRun bool) string {
	var b strings.Builder
	if dryRun {
		fmt.Fprintf(&b, "Would import %s from %s into namespace %q:\n", rowCount(r.Rows), r.File, r.Namespace)
	} else {
		fmt.Fprintf(&b, "Importing %s from %s into namespace %q (%s):\n", rowCount(r.Rows), r.File, r.Namespace, r.Status)
	}
	for _, col := range r.Columns {
		use := col.Use
		if col.Language != "" {
			use = col.Language + " translation"
		}
		fmt.Fprintf(&b, "  %-24s %s\n", col.Header, use)
	}
	if !dryRun {
		fmt.Fprintf(&b, "Follow it with `norcube langsync terms list --namespace %s`.", r.Namespace)
	}
	return strings.TrimRight(b.String(), "\n")
}

func rowCount(n int) string {
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}

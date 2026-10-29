package cmdutil

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// PageFlags are the pagination flags every list command takes.
//
// Lists print as plain arrays (the JSON shape scripts already rely on);
// when more results exist the next cursor goes to stderr: a sentence on a
// table, `nextCursor=<cursor>` for json, yaml and text, so a script can
// capture it with 2>.
type PageFlags struct {
	PageSize int
	Cursor   string
	AllPages bool
	MaxItems int

	defaultPageSize int
	defaultMaxItems int
}

// AddPageFlags registers --page-size (alias --limit), --cursor, --all-pages
// and --max-items. A defaultPageSize of 0 means the API picks the page
// size, and --page-size isn't offered.
func AddPageFlags(cmd *cobra.Command, pf *PageFlags, defaultPageSize, defaultMaxItems int) {
	pf.defaultPageSize, pf.defaultMaxItems = defaultPageSize, defaultMaxItems
	if defaultPageSize > 0 {
		cmd.Flags().IntVar(&pf.PageSize, "page-size", 0, fmt.Sprintf("Items per request (0 = %d)", defaultPageSize))
		cmd.Flags().IntVar(&pf.PageSize, "limit", 0, "Same as --page-size")
	}
	cmd.Flags().StringVar(&pf.Cursor, "cursor", "", "Continue from the cursor a previous page printed")
	cmd.Flags().BoolVar(&pf.AllPages, "all-pages", false, "Follow cursors until every page is fetched (capped by --max-items)")
	cmd.Flags().IntVar(&pf.MaxItems, "max-items", 0, fmt.Sprintf("Cap for --all-pages (0 = %d)", defaultMaxItems))
}

// Page is what Paginate collected.
type Page[T any] struct {
	Items []T
	// NextCursor continues where Items stop; empty on the last page.
	NextCursor string
	// Truncated: --all-pages stopped at the --max-items cap.
	Truncated bool
	Cap       int
}

// FetchFunc gets one page starting at cursor ("" for the first).
type FetchFunc[T any] func(ctx context.Context, cursor string, pageSize int) (items []T, next string, err error)

// Paginate fetches one page, or with --all-pages every page up to
// --max-items. Items is never nil, so an empty list prints as [].
func Paginate[T any](ctx context.Context, pf PageFlags, fetch FetchFunc[T]) (Page[T], error) {
	if pf.PageSize < 0 || pf.MaxItems < 0 {
		return Page[T]{}, clierr.Usage(fmt.Errorf("--page-size and --max-items must not be negative"))
	}
	size := pf.PageSize
	if size == 0 {
		size = pf.defaultPageSize
	}
	limit := pf.MaxItems
	if limit == 0 {
		limit = pf.defaultMaxItems
	}
	p := Page[T]{Items: []T{}, Cap: limit}
	cursor := pf.Cursor
	for {
		items, next, err := fetch(ctx, cursor, size)
		if err != nil {
			return Page[T]{}, err
		}
		p.Items = append(p.Items, items...)
		p.NextCursor = next
		if !pf.AllPages || next == "" {
			return p, nil
		}
		if len(p.Items) >= limit {
			p.Truncated = true
			return p, nil
		}
		cursor = next
	}
}

// Print prints the page's items through view (a table over them) and the
// "more results" note on stderr.
func (p Page[T]) Print(cmd *cobra.Command, view any) error {
	if err := Print(cmd, view); err != nil {
		return err
	}
	p.Note(cmd)
	return nil
}

// Note writes the "more results" note for the page to stderr.
func (p Page[T]) Note(cmd *cobra.Command) {
	if p.NextCursor == "" {
		return
	}
	w := Stderr(cmd)
	if !Human(cmd) {
		fmt.Fprintf(w, "nextCursor=%s\n", p.NextCursor)
		return
	}
	if p.Truncated {
		fmt.Fprintf(w, "\nStopped at --max-items=%d. Re-run with a higher --max-items, or --cursor %s to continue.\n", p.Cap, p.NextCursor)
		return
	}
	fmt.Fprintf(w, "\nMore results available. Re-run with --cursor %s, or --all-pages to fetch them all.\n", p.NextCursor)
}

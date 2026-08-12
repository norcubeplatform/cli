package backup

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/output"
)

// maxBackupItems caps how many jobs the CLI will accumulate in one
// invocation when --all-pages is set, so a user with millions of historical
// jobs doesn't accidentally OOM their terminal. Adjust with --max-items.
const maxBackupItems = 10000

// defaultPageLimit is sent as ?limit= when --limit isn't explicitly passed.
// The backend SQL takes LIMIT straight from the request, so omitting the
// param causes a `LIMIT 0` query that returns nothing — see the discussion
// in apps/snapdb/internal/handler/backuphandler/handler.go (listJobsDefaultLimit).
// We send our own default so the CLI works even against backend builds
// that haven't picked up the clamp yet.
const defaultPageLimit = 50

func newBackupListCmd() *cobra.Command {
	var datasourceIDs []string
	var limit int
	var cursor string
	var allPages bool
	var maxItems int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backup jobs across the active organization, newest first",
		Long: `Lists backup jobs sorted by created_at descending. By default lists
across every data source in the active organization — pass --datasource
one or more times to filter to a subset. The backend paginates
with an opaque "next" cursor; by default this command fetches one page
and prints a hint to stderr if there are more results. Use --all-pages
to follow the cursor until exhausted, with a safety cap of --max-items.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}

			cap := maxItems
			if cap <= 0 {
				cap = maxBackupItems
			}

			// Initialise non-nil so `-o json` on an empty result prints "[]"
			// rather than "null" — saves every downstream `jq` invocation
			// from having to handle both shapes.
			jobs := []snapdb.DtoBackupJob{}
			nextCursor := cursor
			truncated := false

			for {
				params := &snapdb.GetBackupsParams{}
				effectiveLimit := limit
				if effectiveLimit <= 0 {
					effectiveLimit = defaultPageLimit
				}
				params.Limit = &effectiveLimit
				if nextCursor != "" {
					params.Cursor = &nextCursor
				}
				if len(datasourceIDs) > 0 {
					ids := append([]string(nil), datasourceIDs...)
					params.DatasourceIDs = &ids
				}

				res, err := c.client.GetBackupsWithResponse(cmd.Context(), params)
				if err != nil {
					return err
				}
				if res.JSON200 == nil {
					return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
				}
				jobs = append(jobs, res.JSON200.List...)

				next := res.JSON200.Cursors.Next
				if !allPages || next == nil || *next == "" {
					if next != nil && *next != "" {
						nextCursor = *next
					} else {
						nextCursor = ""
					}
					break
				}
				if len(jobs) >= cap {
					truncated = true
					nextCursor = *next
					break
				}
				nextCursor = *next
			}

			flags := clictx.Get(cmd)
			err = output.PrintPaged(cmd.OutOrStdout(), c.output, flags.NoPager, output.Table[snapdb.DtoBackupJob]{
				Headers:   []string{"DATASOURCE", "STATUS", "TEST", "TRIGGER", "STARTED", "DURATION", "SIZE", "JOB_ID"},
				MaxWidths: []int{32, 0, 0, 0, 0, 0, 0, 0},
				Style:     output.Style{StatusColumn: 1},
				Rows: func(j snapdb.DtoBackupJob) []string {
					return []string{
						j.DatasourceName,
						string(j.JobStatus),
						formatVerify(j),
						string(j.JobTrigger),
						formatTimestamp(j.JobStartedAt),
						formatDurationMs(j.JobDurationMs),
						formatBytes(j.JobBytesWritten),
						j.JobId,
					}
				},
				Items: jobs,
			})
			if err != nil {
				return err
			}

			// Hints go to stderr so piping the table to jq / grep stays
			// clean. Skip in JSON/YAML modes and when stderr isn't a TTY.
			stderr := cmd.ErrOrStderr()
			if c.output == output.FormatTable && output.IsInteractive(stderr) {
				if truncated {
					fmt.Fprintf(stderr,
						"\nStopped at --max-items=%d. Re-run with --max-items 0 (no cap) or a higher value to fetch the rest.\n",
						cap)
				} else if !allPages && nextCursor != "" {
					fmt.Fprintf(stderr,
						"\nMore results available. Re-run with --cursor %s, or --all-pages to follow until exhausted.\n",
						nextCursor)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&datasourceIDs, "datasource", nil, "Filter to one or more data source IDs (repeatable; default: every data source in the active org)")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max items per page (0 = backend default)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue from a previous page's `next` cursor")
	cmd.Flags().BoolVar(&allPages, "all-pages", false, "Follow `next` cursors and return every page (may be many requests)")
	cmd.Flags().IntVar(&maxItems, "max-items", 0, "Safety cap for --all-pages (0 = use built-in default, currently 10000)")
	return cmd
}

func newBackupDownloadCmd() *cobra.Command {
	var datasourceID string
	var filePath string

	cmd := &cobra.Command{
		Use:   "download <job-id>",
		Short: "Download a backup artifact to a local file",
		Long: `Download the artifact of a succeeded backup job.

The backend issues a short-lived presigned URL for the backup object and
the CLI streams it to disk. The bytes come straight from storage, not
through the Norcube API. The default filename is taken from the object
name; override it with --file, or pass --file - to stream to stdout
(for piping into pg_restore/mongorestore).

Backups stored in customer-managed buckets can't be downloaded this way:
Norcube deliberately holds no read credentials for your bucket, so fetch
the object directly from your own storage instead.`,
		Example: `  # Download next to you, keeping the object name
  norcube backup download 6a1b2c3d-… --datasource 9f8e7d6c-…

  # Pipe straight into a restore (pg_dump custom format; decompression must
  # match the policy's compression setting, gzip here)
  norcube backup download 6a1b2c3d-… -d 9f8e7d6c-… --file - | gunzip | pg_restore -d "$DSN"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID := args[0]
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}

			res, err := c.client.GenerateBackupDownloadLinkWithResponse(cmd.Context(), datasourceID, jobID)
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON404, res.JSON500)
			}
			link := res.JSON200

			// The backend returns HTTP 200 with ok=false for domain failures
			// (job not finished, customer-managed bucket, …) so it can attach
			// a structured error code. Surface those as real CLI errors.
			if link.Ok == nil || !*link.Ok {
				return downloadLinkError(link)
			}
			if link.Url == nil || *link.Url == "" {
				return fmt.Errorf("backup: download link response was ok but carried no URL")
			}

			return streamDownload(cmd, *link.Url, link.SizeBytes, jobID, filePath)
		},
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Datasource the backup belongs to (id)")
	cmd.Flags().StringVar(&filePath, "file", "", "Write to this path instead of the object name; use - for stdout")
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}

// downloadLinkError translates the backend's structured ok=false answer into
// an actionable message. Unknown codes fall through with code + message so a
// newer backend still produces something useful.
func downloadLinkError(link *snapdb.DatasourcehandlerPayloadGenerateDownloadLinkResponse) error {
	code := deref(link.ErrorCode)
	msg := deref(link.ErrorMessage)
	switch code {
	case "not_found":
		return fmt.Errorf("backup job not found; check the job id and --datasource (see `norcube backup list`)")
	case "not_succeeded":
		return fmt.Errorf("this backup didn't succeed, so there is no artifact to download")
	case "missing_artifact":
		return fmt.Errorf("the backup succeeded but its artifact is gone; it may have been expired by retention")
	case "customer_managed_storage":
		return fmt.Errorf("this backup lives in your own bucket and Norcube holds no read credentials for it; download the object directly from your storage")
	default:
		if msg == "" {
			msg = "download link could not be generated"
		}
		if code != "" {
			return fmt.Errorf("backup: %s (%s)", msg, code)
		}
		return fmt.Errorf("backup: %s", msg)
	}
}

// streamDownload fetches the presigned URL and writes it to the chosen
// destination. Presigned URLs embed their own auth, so this request goes to
// storage directly with no bearer token attached.
func streamDownload(cmd *cobra.Command, rawURL string, sizeBytes *int, jobID, filePath string) error {
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: storage returned %s", resp.Status)
	}

	// --file - streams raw bytes to stdout for piping; no progress, no
	// completion message on stdout (that would corrupt the stream).
	if filePath == "-" {
		_, err := io.Copy(cmd.OutOrStdout(), resp.Body)
		return err
	}

	dest := filePath
	if dest == "" {
		dest = objectBasename(rawURL, jobID)
	}

	// 0600: the artifact is a database dump; nobody else on the machine
	// has any business reading it.
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists; pass --file to pick another name", dest)
		}
		return err
	}

	var src io.Reader = resp.Body
	stderr := cmd.ErrOrStderr()
	if output.IsInteractive(stderr) {
		src = &progressReader{r: resp.Body, total: derefInt(sizeBytes), out: stderr}
	}

	written, err := io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// Don't leave a truncated artifact lying around looking like a
		// valid backup.
		_ = os.Remove(dest)
		return fmt.Errorf("download: %w", err)
	}
	if output.IsInteractive(stderr) {
		fmt.Fprint(stderr, "\r\033[K")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Downloaded %s (%s).\n", dest, formatBytes(int(written)))
	return nil
}

// objectBasename derives a local filename from the presigned URL's object
// key, falling back to the job id when the URL doesn't parse.
func objectBasename(rawURL, jobID string) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		if base := path.Base(u.Path); base != "" && base != "/" && base != "." {
			return base
		}
	}
	return jobID + ".backup"
}

// progressReader counts bytes as they stream through and repaints a one-line
// progress indicator on stderr. Total may be 0 when the backend didn't know
// the size, in which case only the running count is shown.
type progressReader struct {
	r       io.Reader
	total   int
	written int64
	out     io.Writer
	lastPct int
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.written += int64(n)
	if p.total > 0 {
		pct := int(p.written * 100 / int64(p.total))
		if pct != p.lastPct {
			p.lastPct = pct
			fmt.Fprintf(p.out, "\r\033[K%s / %s (%d%%)", formatBytes(int(p.written)), formatBytes(p.total), pct)
		}
	} else if p.written%(4<<20) < int64(n) { // roughly every 4 MiB
		fmt.Fprintf(p.out, "\r\033[K%s", formatBytes(int(p.written)))
	}
	return n, err
}

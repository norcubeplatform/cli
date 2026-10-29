package backup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
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

func newBackupListCmd(d deps) *cobra.Command {
	var datasourceIDs []string
	var pages cmdutil.PageFlags

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backup jobs across the active organization, newest first",
		Long: `Lists backup jobs sorted by created_at descending. By default lists
across every data source in the active organization — pass --datasource
one or more times to filter to a subset. The backend paginates
with an opaque "next" cursor; by default this command fetches one page
and prints the next cursor to stderr if there are more results. Use
--all-pages to follow the cursor until exhausted, with a safety cap of
--max-items.`,
		Example: `  norcube backup jobs list
  norcube backup jobs list --datasource 9f8e7d6c-… --all-pages
  norcube backup jobs list -o json --query "[?jobStatus=='failed'].jobId"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]snapdb.DtoBackupJob, string, error) {
				params := &snapdb.GetBackupsParams{Limit: &size}
				if cursor != "" {
					params.Cursor = &cursor
				}
				if len(datasourceIDs) > 0 {
					ids := append([]string(nil), datasourceIDs...)
					params.DatasourceIDs = &ids
				}
				res, err := c.API.GetBackupsWithResponse(ctx, params)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
				}
				next := ""
				if n := res.JSON200.Cursors.Next; n != nil {
					next = *n
				}
				return res.JSON200.List, next, nil
			})
			if err != nil {
				return err
			}
			return page.Print(cmd, output.Table[snapdb.DtoBackupJob]{
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
				Items: page.Items,
			})
		},
	}
	cmd.Flags().StringSliceVar(&datasourceIDs, "datasource", nil, "Filter to one or more data source IDs (repeatable; default: every data source in the active org)")
	cmdutil.AddPageFlags(cmd, &pages, defaultPageLimit, maxBackupItems)
	return cmd
}

func newBackupDownloadCmd(d deps) *cobra.Command {
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
  norcube backup jobs download 6a1b2c3d-… --datasource 9f8e7d6c-…

  # Pipe straight into a restore (pg_dump custom format; decompression must
  # match the policy's compression setting, gzip here)
  norcube backup jobs download 6a1b2c3d-… -d 9f8e7d6c-… --file - | gunzip | pg_restore -d "$DSN"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID := args[0]
			c, err := d.client(cmd)
			if err != nil {
				return err
			}

			res, err := c.API.GenerateBackupDownloadLinkWithResponse(cmd.Context(), datasourceID, jobID)
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
	code := cmdutil.Deref(link.ErrorCode)
	msg := cmdutil.Deref(link.ErrorMessage)
	switch code {
	case "not_found":
		return fmt.Errorf("backup job not found; check the job id and --datasource (see `norcube backup jobs list`)")
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
	httpc, err := clictx.Get(cmd).DownloadClient()
	if err != nil {
		return err
	}
	resp, err := httpc.Do(req)
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
		_, err := cmdutil.SaveStream(cmd, resp.Body, "-", 0)
		return err
	}

	dest := filePath
	if dest == "" {
		dest = objectBasename(rawURL, jobID)
	}

	var src io.Reader = resp.Body
	stderr := cmd.ErrOrStderr()
	if output.IsInteractive(stderr) {
		src = &progressReader{r: resp.Body, total: cmdutil.Deref(sizeBytes), out: stderr}
	}

	// 0600: the artifact is a database dump; nobody else on the machine
	// has any business reading it.
	saved, err := cmdutil.SaveStream(cmd, src, dest, 0o600)
	if output.IsInteractive(stderr) {
		fmt.Fprint(stderr, "\r\033[K")
	}
	if err != nil {
		if clierr.IsUsage(err) {
			return fmt.Errorf("%s already exists; pass --file to pick another name", dest)
		}
		return fmt.Errorf("download: %w", err)
	}
	return cmdutil.Print(cmd, output.Done{
		Message: fmt.Sprintf("Downloaded %s (%s, sha256 %s).", saved.Path, formatBytes(int(saved.Bytes)), saved.SHA256),
		Value:   saved,
		Fields:  []output.Field{{Value: saved.Path}, {Value: fmt.Sprint(saved.Bytes)}, {Value: saved.SHA256}},
	})
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

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/apierror"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/registry"
	"github.com/norcubeplatform/cli/internal/service"
)

// rawAPI sends hand-built requests to one service with the invocation's
// credentials and transport.
type rawAPI struct {
	baseURL string
	http    *http.Client
	edit    service.RequestEditor
}

func newRawAPI(baseURL string, httpc *http.Client, edit service.RequestEditor) (*rawAPI, error) {
	return &rawAPI{baseURL: strings.TrimRight(baseURL, "/"), http: httpc, edit: edit}, nil
}

// maxAPIPages bounds --paginate so a server that keeps answering with a
// cursor can't loop forever.
const maxAPIPages = 1000

func newAPICmd() *cobra.Command {
	var (
		fields   []string
		typed    []string
		headers  []string
		input    string
		paginate bool
	)
	var names []string
	for _, s := range registry.Callable() {
		names = append(names, s.Command)
	}
	cmd := &cobra.Command{
		Use:   "api <service> [METHOD] <path>",
		Short: "Make an authenticated request to a Norcube API",
		Long: `Sends one request to a Norcube service with the profile's credentials,
organization, timeouts and retries, and prints the JSON response. Use it
for endpoints the CLI has no command for yet.

Services: ` + strings.Join(names, ", ") + `. The path is relative to the
service's API base URL (see ` + "`norcube config list`" + `). METHOD defaults to
GET, or POST when fields or --input are given.

-f key=value adds a string field and -F key=value a typed one (true,
false, null, numbers, or @file for a file's contents). Fields become query
parameters on GET, HEAD and DELETE and a JSON body otherwise. --input
sends a file (or - for stdin) as the body instead.

--paginate follows the service's cursor (nextCursor, cursors.next or the
nextCursorCreatedAt/nextCursorId pair) and merges the arrays of every page.`,
		Example: `  # List data sources in the profile's organization
  norcube api backup GET /datasources

  # Check one domain in another organization
  norcube api domainradar POST /check -f domain=example.com --org acme

  # Every bulk check, all pages, only the ids
  norcube api domainradar /check/bulk --paginate --query "list[].id"

  # A request body from a file
  norcube api langsync POST /namespaces --input namespace.json`,
		Args: cobra.RangeArgs(2, 3),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			svc, ok := registry.Lookup(args[0])
			if !ok || svc.Audience == "" || svc.Command == "" {
				return nil, clierr.Usage(fmt.Errorf("unknown service %q (want one of: %s)", args[0], strings.Join(names, ", ")))
			}
			method, path := "", args[len(args)-1]
			if len(args) == 3 {
				method = strings.ToUpper(args[1])
			}
			if !strings.HasPrefix(path, "/") {
				return nil, clierr.Usage(fmt.Errorf("path must start with / and be relative to the %s API, got %q", svc.Name, path))
			}

			params, err := parseAPIFields(fields, typed)
			if err != nil {
				return nil, clierr.Usage(err)
			}
			if input != "" && len(params) > 0 {
				return nil, clierr.Usage(errors.New("use either fields (-f/-F) or --input, not both"))
			}
			if method == "" {
				method = http.MethodGet
				if input != "" || len(params) > 0 {
					method = http.MethodPost
				}
			}
			hdr, err := parseAPIHeaders(headers)
			if err != nil {
				return nil, clierr.Usage(err)
			}
			var body []byte
			if input != "" {
				if body, err = readAPIInput(cmd, input); err != nil {
					return nil, clierr.Usage(err)
				}
			}

			c, err := service.New(cmd, svc.Key, newRawAPI)
			if err != nil {
				return nil, err
			}
			req := apiRequest{method: method, path: path, params: params, body: body, headers: hdr}
			if !paginate {
				return c.API.do(cmd, svc.Name, req, nil)
			}
			return c.API.paginate(cmd, svc.Name, req)
		}),
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "Add a string field key=value (repeatable)")
	cmd.Flags().StringArrayVarP(&typed, "raw-field", "F", nil, "Add a typed field key=value: true, false, null, a number or @file (repeatable)")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, `Add a request header "Key: Value" (repeatable)`)
	cmd.Flags().StringVar(&input, "input", "", "Send this file as the request body, - for stdin")
	cmd.Flags().BoolVar(&paginate, "paginate", false, "Follow cursors and merge every page")
	cmd.ValidArgsFunction = func(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return names, cobra.ShellCompDirectiveNoFileComp
		}
		if len(args) == 1 {
			return []cobra.Completion{"GET", "POST", "PUT", "PATCH", "DELETE"}, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}

type apiRequest struct {
	method  string
	path    string
	params  map[string]any
	body    []byte
	headers http.Header
}

// do sends req (plus extra query parameters) and decodes the JSON answer.
func (a *rawAPI) do(cmd *cobra.Command, service string, req apiRequest, extra url.Values) (any, error) {
	u, err := url.Parse(a.baseURL + req.path)
	if err != nil {
		return nil, clierr.Usage(fmt.Errorf("path: %w", err))
	}
	q := u.Query()
	body := req.body
	queryParams := req.method == http.MethodGet || req.method == http.MethodHead || req.method == http.MethodDelete
	if len(req.params) > 0 {
		if queryParams {
			for k, v := range req.params {
				q.Set(k, fmt.Sprint(v))
			}
		} else if body, err = json.Marshal(req.params); err != nil {
			return nil, err
		}
	}
	for k, vs := range extra {
		q[k] = vs
	}
	u.RawQuery = q.Encode()

	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	hreq, err := http.NewRequestWithContext(cmd.Context(), req.method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Accept", "application/json")
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range req.headers {
		hreq.Header[k] = vs
	}
	if err := a.edit(cmd.Context(), hreq); err != nil {
		return nil, err
	}
	resp, err := a.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, apierror.Format(service, resp, raw)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{"status": resp.StatusCode}, nil
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		// Not JSON: hand back the text unchanged.
		return string(raw), nil
	}
	return out, nil
}

// paginate fetches page after page and merges them: the arrays of every
// page are concatenated, the other fields come from the last page, and
// the cursor fields are dropped.
func (a *rawAPI) paginate(cmd *cobra.Command, service string, req apiRequest) (any, error) {
	var merged map[string]any
	var extra url.Values
	for page := 1; ; page++ {
		got, err := a.do(cmd, service, req, extra)
		if err != nil {
			return nil, err
		}
		obj, ok := got.(map[string]any)
		if !ok {
			// A bare array or text has no cursor: one page is all.
			if merged == nil {
				return got, nil
			}
			return nil, fmt.Errorf("page %d of a paginated response is not a JSON object", page)
		}
		next := nextPageParams(obj)
		if merged == nil {
			merged = obj
		} else {
			for k, v := range obj {
				if arr, ok := v.([]any); ok {
					if prev, ok := merged[k].([]any); ok {
						merged[k] = append(prev, arr...)
						continue
					}
				}
				merged[k] = v
			}
		}
		if next == nil {
			break
		}
		if page >= maxAPIPages {
			fmt.Fprintf(cmd.ErrOrStderr(), "Stopped after %d pages; more results exist.\n", maxAPIPages)
			break
		}
		extra = next
	}
	for _, k := range []string{"nextCursor", "next_cursor", "nextCursorCreatedAt", "nextCursorId"} {
		delete(merged, k)
	}
	if c, ok := merged["cursors"].(map[string]any); ok {
		delete(c, "next")
	}
	return merged, nil
}

// nextPageParams reads the cursor of a page in any of the shapes Norcube
// services use; nil on the last page.
func nextPageParams(obj map[string]any) url.Values {
	str := func(v any) string { s, _ := v.(string); return s }
	if s := str(obj["nextCursor"]); s != "" {
		return url.Values{"cursor": {s}}
	}
	if s := str(obj["next_cursor"]); s != "" {
		return url.Values{"cursor": {s}}
	}
	if c, ok := obj["cursors"].(map[string]any); ok {
		if s := str(c["next"]); s != "" {
			return url.Values{"cursor": {s}}
		}
	}
	at, id := str(obj["nextCursorCreatedAt"]), str(obj["nextCursorId"])
	if at != "" && id != "" {
		return url.Values{"cursorCreatedAt": {at}, "cursorId": {id}}
	}
	return nil
}

func parseAPIFields(strs, typed []string) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range strs {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("-f %q: want key=value", f)
		}
		out[k] = v
	}
	for _, f := range typed {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("-F %q: want key=value", f)
		}
		val, err := typedValue(v)
		if err != nil {
			return nil, fmt.Errorf("-F %s: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}

func typedValue(v string) (any, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if strings.HasPrefix(v, "@") {
		b, err := os.ReadFile(v[1:]) // #nosec G304 -- a path the user passed
		if err != nil {
			return nil, err
		}
		return string(b), nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, nil
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f, nil
	}
	return v, nil
}

// apiHeadersNotSettable are set by the CLI itself.
var apiHeadersNotSettable = map[string]bool{"Authorization": true, "Cookie": true, "Host": true, "Content-Length": true}

func parseAPIHeaders(list []string) (http.Header, error) {
	h := http.Header{}
	for _, l := range list {
		k, v, ok := strings.Cut(l, ":")
		k = http.CanonicalHeaderKey(strings.TrimSpace(k))
		if !ok || k == "" {
			return nil, fmt.Errorf("-H %q: want \"Key: Value\"", l)
		}
		if apiHeadersNotSettable[k] {
			return nil, fmt.Errorf("-H %s: the CLI sets this header itself", k)
		}
		h.Add(k, strings.TrimSpace(v))
	}
	return h, nil
}

func readAPIInput(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	return os.ReadFile(path) // #nosec G304 -- a path the user passed
}

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// fakeService plays a Norcube product API for `nrc api`.
type fakeService struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (f *fakeService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.body = append(f.body, string(b))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/things":
		_ = json.NewEncoder(w).Encode(map[string]string{"q": r.URL.Query().Get("name")})
	case "/echo":
		_, _ = w.Write(b)
	case "/pages":
		// Three pages linked by nextCursor.
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"items":[1,2],"nextCursor":"c2","total":5}`)
		case "c2":
			fmt.Fprint(w, `{"items":[3,4],"nextCursor":"c3","total":5}`)
		default:
			fmt.Fprint(w, `{"items":[5],"total":5}`)
		}
	case "/keyset":
		if r.URL.Query().Get("cursorId") == "" {
			fmt.Fprint(w, `{"list":["a"],"nextCursorCreatedAt":"2026-01-01T00:00:00Z","nextCursorId":"x"}`)
		} else {
			fmt.Fprint(w, `{"list":["b"]}`)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"type":"NOT_FOUND","msg":"no such thing"}`)
	}
}

func (f *fakeService) last() (*http.Request, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1], f.body[len(f.body)-1]
}

func newFakeService(t *testing.T) *fakeService {
	t.Helper()
	f := &fakeService{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("NORCUBE_SNAPDB_URL", srv.URL)
	signIn(t)
	return f
}

func TestAPIGetSendsFieldsAsQueryWithAuth(t *testing.T) {
	f := newFakeService(t)
	out, stderr, err := run(t, "api", "backup", "GET", "/things", "-f", "name=prod db", "-o", "json")
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	req, _ := f.last()
	if req.Method != http.MethodGet || req.URL.Query().Get("name") != "prod db" {
		t.Errorf("sent %s %s", req.Method, req.URL)
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
		t.Error("no bearer token")
	}
	if !strings.Contains(out, `"q": "prod db"`) {
		t.Errorf("stdout %q", out)
	}
}

func TestAPIPostSendsTypedJSONBody(t *testing.T) {
	f := newFakeService(t)
	_, stderr, err := run(t, "api", "snapdb", "POST", "/echo", "-f", "name=x", "-F", "count=3", "-F", "on=true", "-F", "gone=null", "-o", "json")
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	req, body := f.last()
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if req.Header.Get("Content-Type") != "application/json" || got["name"] != "x" || got["count"] != float64(3) || got["on"] != true || got["gone"] != nil {
		t.Errorf("body %v", got)
	}
}

func TestAPIPaginateMergesPages(t *testing.T) {
	newFakeService(t)
	out, stderr, err := run(t, "api", "backup", "/pages", "--paginate", "-o", "json")
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got["items"]) != "[1 2 3 4 5]" || got["nextCursor"] != nil || fmt.Sprint(got["total"]) != "5" {
		t.Errorf("merged %v", got)
	}

	out, _, err = run(t, "api", "backup", "/keyset", "--paginate", "--query", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(out), "") != `["a","b"]` {
		t.Errorf("keyset pages %q", out)
	}
}

func TestAPIErrors(t *testing.T) {
	newFakeService(t)
	_, _, err := run(t, "api", "backup", "/missing")
	var apiErr *clierr.Error
	if !errors.As(err, &apiErr) || apiErr.ExitCode() != clierr.ExitNotFound {
		t.Errorf("404: want exit %d, got %v", clierr.ExitNotFound, err)
	}
	for _, args := range [][]string{
		{"api", "nope", "/x"},
		{"api", "backup", "datasources"},
		{"api", "backup", "/x", "-H", "Authorization: Bearer mine"},
		{"api", "backup", "POST", "/x", "-f", "a=b", "--input", "-"},
		{"api", "web_app", "/x"},
	} {
		_, _, err := run(t, args...)
		if !clierr.IsUsage(err) {
			t.Errorf("%v: want a usage error, got %v", args, err)
		}
	}
}

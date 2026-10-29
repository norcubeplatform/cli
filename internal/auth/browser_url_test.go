package auth

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/transport"
)

// The sign-in page shows the device and records it on the session, so
// the login URL names this machine and the CLI build.
func TestLoginURLNamesTheDevice(t *testing.T) {
	raw := buildLoginURL("https://app.example.test", 63018, "s123")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/cli-login" {
		t.Errorf("path = %q", u.Path)
	}
	q := u.Query()
	if q.Get("port") != "63018" || q.Get("state") != "s123" {
		t.Errorf("port/state = %q/%q", q.Get("port"), q.Get("state"))
	}
	if got, want := q.Get("client"), transport.UserAgent(); got != want {
		t.Errorf("client = %q, want %q", got, want)
	}
	if host, err := os.Hostname(); err == nil && q.Get("host") != strings.TrimSpace(host) {
		t.Errorf("host = %q, want %q", q.Get("host"), host)
	}
}

func TestTruncateKeepsWholeCharacters(t *testing.T) {
	long := strings.Repeat("a", maxLoginParam-1) + "é"
	got := truncate(long)
	if len(got) > maxLoginParam || !strings.HasPrefix(long, got) || strings.ContainsRune(got, '�') {
		t.Errorf("truncate split a character: %q", got[len(got)-3:])
	}
	if truncate("short") != "short" {
		t.Error("short value changed")
	}
}

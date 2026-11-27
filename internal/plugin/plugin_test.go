package plugin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakePlugin writes a shell-script plugin into a fresh plugin directory.
func fakePlugin(t *testing.T, name, info string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugins")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- a plugin directory must be traversable
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = "__plugin-info" ]; then echo '` + info + `'; exit 0; fi
echo "args:$*"
echo "protocol:$NRC_PLUGIN_PROTOCOL profile:$NRC_PROFILE"
if [ "$1" = "fail" ]; then exit 7; fi
`
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	t.Setenv(DirEnv, dir)
	return p
}

func TestLocateHandshakeRun(t *testing.T) {
	fakePlugin(t, "nrc-example", `{"name":"nrc-example","version":"1.2.3","protocol":1}`)
	path, err := Locate("nrc-example")
	if err != nil {
		t.Fatal(err)
	}
	info, err := Handshake(context.Background(), path, "nrc-example")
	if err != nil || info.Version != "1.2.3" {
		t.Fatalf("handshake: %+v %v", info, err)
	}
	var out bytes.Buffer
	code, err := Run(context.Background(), path, []string{"authz", "run"}, []string{"NRC_PLUGIN_PROTOCOL=1", "NRC_PROFILE=work"}, nil, &out, &out)
	if err != nil || code != 0 {
		t.Fatalf("run: %d %v", code, err)
	}
	if !strings.Contains(out.String(), "args:authz run") || !strings.Contains(out.String(), "protocol:1 profile:work") {
		t.Errorf("output %q", out.String())
	}
	code, _ = Run(context.Background(), path, []string{"fail"}, nil, nil, &out, &out)
	if code != 7 {
		t.Errorf("exit code %d, want the plugin's 7", code)
	}
}

func TestHandshakeRefusesWrongNameOrProtocol(t *testing.T) {
	for _, info := range []string{`{"name":"other","protocol":1}`, `{"name":"nrc-example","protocol":99}`, `not json`} {
		p := fakePlugin(t, "nrc-example", info)
		if _, err := Handshake(context.Background(), p, "nrc-example"); err == nil {
			t.Errorf("%s accepted", info)
		}
	}
}

func TestRefusesWritableByOthers(t *testing.T) {
	p := fakePlugin(t, "nrc-example", `{"name":"nrc-example","protocol":1}`)
	if err := os.Chmod(filepath.Dir(p), 0o777); err != nil { // #nosec G302 -- the condition under test
		t.Fatal(err)
	}
	if _, err := Locate("nrc-example"); err == nil || !strings.Contains(err.Error(), "writable by other users") {
		t.Errorf("world-writable directory accepted: %v", err)
	}
}

func TestNotInstalled(t *testing.T) {
	t.Setenv(DirEnv, t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if _, err := Locate("nrc-example"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("err %v, want ErrNotInstalled", err)
	}
}

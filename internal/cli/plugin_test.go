package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/plugin"
	"github.com/norcubeplatform/cli/internal/registry"
)

func TestPluginCommandPassesArgsAndExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin")
	}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	script := "#!/bin/sh\nif [ \"$1\" = __plugin-info ]; then echo '{\"name\":\"nrc-demo\",\"version\":\"0.1.0\",\"protocol\":1}'; exit 0; fi\n" +
		"echo \"args=$* protocol=$NRC_PLUGIN_PROTOCOL bin=${NRC_BIN:+set}\"\n[ \"$1\" = fail ] && exit 9\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "nrc-demo"), []byte(script), 0o755); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	t.Setenv(plugin.DirEnv, dir)
	svc := registry.Service{Key: "demo", Name: "Demo", Command: "demo", Plugin: "nrc-demo"}

	cmd := newPluginCmd(svc)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"run", "--flag", "x"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "args=run --flag x protocol=1 bin=set") {
		t.Errorf("plugin output %q", got)
	}

	cmd = newPluginCmd(svc)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"fail"})
	var pe clierr.PassthroughExit
	if err := cmd.Execute(); !errors.As(err, &pe) || pe.Code != 9 {
		t.Fatalf("err %v, want the plugin's exit 9", err)
	}
	var stderr bytes.Buffer
	if code := PrintError(&stderr, pe, false); code != 9 || stderr.Len() != 0 {
		t.Errorf("PrintError: code %d, printed %q", code, stderr.String())
	}
}

func TestPluginCommandNotInstalled(t *testing.T) {
	t.Setenv(plugin.DirEnv, t.TempDir())
	t.Setenv("PATH", t.TempDir())
	cmd := newPluginCmd(registry.Service{Key: "demo", Name: "Demo", Command: "demo", Plugin: "nrc-demo"})
	cmd.SetArgs(nil)
	var ce *clierr.Error
	if err := cmd.Execute(); !errors.As(err, &ce) || ce.Code != clierr.CodeNotFound || ce.Hint == "" {
		t.Fatalf("err %v", err)
	}
}

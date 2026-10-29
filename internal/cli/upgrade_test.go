package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creativeprojects/go-selfupdate"
)

// fakeAsset and fakeRelease stand in for a GitHub release so the updater
// can be driven without the network.
type fakeAsset struct {
	id   int64
	name string
	data []byte
}

func (a fakeAsset) GetID() int64                  { return a.id }
func (a fakeAsset) GetName() string               { return a.name }
func (a fakeAsset) GetSize() int                  { return len(a.data) }
func (a fakeAsset) GetBrowserDownloadURL() string { return "https://example.invalid/" + a.name }

type fakeRelease struct {
	tag    string
	assets []fakeAsset
}

func (r fakeRelease) GetID() int64              { return 1 }
func (r fakeRelease) GetTagName() string        { return r.tag }
func (r fakeRelease) GetDraft() bool            { return false }
func (r fakeRelease) GetPrerelease() bool       { return false }
func (r fakeRelease) GetPublishedAt() time.Time { return time.Unix(0, 0) }
func (r fakeRelease) GetReleaseNotes() string   { return "" }
func (r fakeRelease) GetName() string           { return r.tag }
func (r fakeRelease) GetURL() string            { return "" }
func (r fakeRelease) GetAssets() []selfupdate.SourceAsset {
	out := make([]selfupdate.SourceAsset, 0, len(r.assets))
	for _, a := range r.assets {
		out = append(out, a)
	}
	return out
}

type fakeSource struct{ rel fakeRelease }

func (s fakeSource) ListReleases(context.Context, selfupdate.Repository) ([]selfupdate.SourceRelease, error) {
	return []selfupdate.SourceRelease{s.rel}, nil
}

func (s fakeSource) DownloadReleaseAsset(_ context.Context, _ *selfupdate.Release, id int64) (io.ReadCloser, error) {
	for _, a := range s.rel.assets {
		if a.id == id {
			return io.NopCloser(bytes.NewReader(a.data)), nil
		}
	}
	return nil, fmt.Errorf("asset %d not found", id)
}

// tarGz builds a release-style archive holding only the norcube binary.
func tarGz(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "norcube", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// releaseWith returns a release laid out like a real one: the archive is
// listed after the packages, SBOMs and signatures, and next to a decoy
// whose name the updater's default suffix matching would accept.
func releaseWith(archive []byte, checksums string) fakeRelease {
	assets := []fakeAsset{
		{id: 1, name: "norcube_0.21.0_amd64.deb", data: []byte("deb")},
		{id: 2, name: "norcube_linux_amd64.tar.gz.sbom.json", data: []byte("{}")},
		{id: 3, name: "checksums.txt.sigstore.json", data: []byte("{}")},
		{id: 4, name: "norcube_linux_amd64", data: []byte("decoy")},
		{id: 5, name: "norcube_linux_amd64.tar.gz", data: archive},
	}
	if checksums != "" {
		assets = append(assets, fakeAsset{id: 6, name: checksumsAsset, data: []byte(checksums)})
	}
	return fakeRelease{tag: "v0.21.0", assets: assets}
}

func runFakeUpgrade(t *testing.T, rel fakeRelease) (string, error) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "norcube")
	if err := os.WriteFile(exe, []byte("old binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	updater, err := selfupdate.NewUpdater(upgradeConfig(fakeSource{rel: rel}, "linux", "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	latest, found, err := updater.DetectLatest(context.Background(), selfupdate.NewRepositorySlug(releaseOwner, releaseRepo))
	if err != nil {
		return exe, err
	}
	if !found {
		t.Fatal("release not found")
	}
	if latest.AssetName != "norcube_linux_amd64.tar.gz" {
		t.Fatalf("picked asset %q, want the linux/amd64 archive", latest.AssetName)
	}
	return exe, updater.UpdateTo(context.Background(), latest, exe)
}

func TestUpgradeVerifiesChecksum(t *testing.T) {
	archive := tarGz(t, []byte("new binary"))
	checksums := sha256Hex([]byte("deb")) + "  norcube_0.21.0_amd64.deb\n" +
		sha256Hex(archive) + "  norcube_linux_amd64.tar.gz\n"

	exe, err := runFakeUpgrade(t, releaseWith(archive, checksums))
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new binary" {
		t.Fatalf("binary = %q, want the archive's content", got)
	}
}

func TestUpgradeRejectsChecksumMismatch(t *testing.T) {
	archive := tarGz(t, []byte("new binary"))
	checksums := sha256Hex([]byte("something else")) + "  norcube_linux_amd64.tar.gz\n"

	exe, err := runFakeUpgrade(t, releaseWith(archive, checksums))
	if err == nil {
		t.Fatal("upgrade succeeded with a wrong checksum")
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "old binary" {
		t.Fatalf("binary was replaced (%q) despite the mismatch", got)
	}
}

func TestUpgradeRejectsArchiveMissingFromChecksums(t *testing.T) {
	archive := tarGz(t, []byte("new binary"))
	checksums := sha256Hex([]byte("deb")) + "  norcube_0.21.0_amd64.deb\n"

	exe, err := runFakeUpgrade(t, releaseWith(archive, checksums))
	if err == nil {
		t.Fatal("upgrade succeeded although checksums.txt has no entry for the archive")
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "old binary" {
		t.Fatalf("binary was replaced (%q)", got)
	}
}

func TestUpgradeRequiresChecksumsFile(t *testing.T) {
	archive := tarGz(t, []byte("new binary"))
	_, err := runFakeUpgrade(t, releaseWith(archive, ""))
	if !errors.Is(err, selfupdate.ErrValidationAssetNotFound) {
		t.Fatalf("err = %v, want ErrValidationAssetNotFound", err)
	}
}

func TestReleaseArchivePattern(t *testing.T) {
	cases := []struct {
		goos, goarch, name string
		want               bool
	}{
		{"linux", "amd64", "norcube_linux_amd64.tar.gz", true},
		{"darwin", "arm64", "norcube_darwin_arm64.tar.gz", true},
		{"windows", "amd64", "norcube_windows_amd64.zip", true},
		{"windows", "amd64", "norcube_windows_amd64.tar.gz", false},
		{"linux", "amd64", "norcube_linux_arm64.tar.gz", false},
		{"linux", "amd64", "norcube_linux_amd64.tar.gz.sbom.json", false},
		{"linux", "amd64", "norcube_0.21.0_amd64.deb", false},
		{"linux", "amd64", "norcube_0.21.0_x86_64.apk", false},
		{"linux", "amd64", "norcube-0.21.0-1.x86_64.rpm", false},
		{"linux", "amd64", "checksums.txt.sigstore.json", false},
	}
	for _, c := range cases {
		re := regexp.MustCompile(releaseArchivePattern(c.goos, c.goarch))
		if got := re.MatchString(c.name); got != c.want {
			t.Errorf("%s/%s %q: match = %v, want %v", c.goos, c.goarch, c.name, got, c.want)
		}
	}
}

func TestManagedInstallReasonFor(t *testing.T) {
	cases := []struct {
		exe, goos, want string
	}{
		{"/opt/homebrew/Caskroom/norcube/0.21.0/norcube", "darwin", "brew upgrade"},
		{"/usr/local/Caskroom/norcube/0.21.0/norcube", "darwin", "brew upgrade"},
		{"/home/linuxbrew/.linuxbrew/Caskroom/norcube/0.21.0/norcube", "linux", "brew upgrade"},
		{"/usr/bin/norcube", "linux", "package manager"},
		{`C:\Users\me\scoop\apps\norcube\0.21.0\norcube.exe`, "windows", "scoop update norcube"},
		{`C:\ProgramData\scoop\apps\norcube\current\norcube.exe`, "windows", "scoop update norcube"},
		{`C:\Users\me\AppData\Local\Microsoft\WinGet\Packages\Norcube.CLI_Microsoft.Winget.Source_8wekyb3d8bbwe\norcube.exe`, "windows", "winget upgrade"},
		{`C:\Users\me\AppData\Local\Microsoft\WinGet\Links\norcube.exe`, "windows", "winget upgrade"},
		{"/Users/me/.norcube/bin/norcube", "darwin", ""},
		{"/usr/local/bin/norcube", "linux", ""},
		{`C:\Users\me\bin\norcube.exe`, "windows", ""},
	}
	for _, c := range cases {
		got := managedInstallReasonFor(c.exe, c.goos)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: reason = %q, want none", c.exe, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: reason = %q, want it to mention %q", c.exe, got, c.want)
		}
	}
}

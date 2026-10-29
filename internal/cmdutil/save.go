package cmdutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// SavedFile is the result of a command that writes a file: what -o json
// prints and what its success message describes.
type SavedFile struct {
	Path   string `json:"path" yaml:"path"`
	Bytes  int64  `json:"bytes" yaml:"bytes"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

// SaveStream writes src to dest and returns what was written.
//
// dest "-" streams the bytes to the command's standard output, for
// piping: they are the command's whole result, so it returns (nil, nil)
// and the caller prints nothing else.
//
// Otherwise dest is created (never overwritten: an existing file is a
// usage error) with mode perm, and the SHA-256 of the content is computed
// on the way. A failed or interrupted write removes the partial file, so a
// truncated download never looks like a complete one.
func SaveStream(cmd *cobra.Command, src io.Reader, dest string, perm os.FileMode) (*SavedFile, error) {
	if dest == "-" {
		_, err := io.Copy(cmd.OutOrStdout(), src)
		return nil, err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) // #nosec G304 -- a path the user chose
	if err != nil {
		if os.IsExist(err) {
			return nil, clierr.Usage(fmt.Errorf("%s already exists; choose another file name", dest))
		}
		return nil, err
	}
	sum := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, sum), src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = cmd.Context().Err()
	}
	if err != nil {
		_ = os.Remove(dest)
		return nil, err
	}
	return &SavedFile{Path: dest, Bytes: written, SHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}

package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"loki/internal/tools"
)

type artifactContextReader struct {
	context context.Context
	reader  io.Reader
}

func (r artifactContextReader) Read(data []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

// Local acquisition retains catalog authority. It copies into the installer's
// owned temporary archive and checks the complete bytes before extraction.
// Exact digest filenames avoid URL-derived traversal and ambiguous mirrors.
func localArtifact(ctx context.Context, directory string, artifact tools.Artifact, output io.Writer) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(directory) {
		return fmt.Errorf("local archives directory must be absolute")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("local archives directory cannot be opened")
	}
	defer root.Close()
	name := artifact.SHA256 + "." + artifact.Format
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Bytes {
		return fmt.Errorf("local archive is missing or differs from the trusted byte length")
	}
	file, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("local archive cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("local archive changed while opening")
	}
	checksum := sha256.New()
	n, err := io.Copy(io.MultiWriter(output, checksum), io.LimitReader(artifactContextReader{context: ctx, reader: file}, artifact.Bytes+1))
	if err != nil {
		return err
	}
	if n != artifact.Bytes || hex.EncodeToString(checksum.Sum(nil)) != artifact.SHA256 {
		return fmt.Errorf("local archive length or SHA-256 mismatch")
	}
	return nil
}

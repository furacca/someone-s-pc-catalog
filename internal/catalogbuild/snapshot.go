// Package catalogbuild verifies the source snapshot used to build an archive.
package catalogbuild

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/furacca/someone-s-pc-catalog/internal/catalogdata"
)

// InventorySnapshot counts files and bytes per top-level directory of a source
// snapshot, and verifies the exact compiled files read by the projection.
func InventorySnapshot(snapshot io.Reader, generatedDir string, languages []string) ([]catalogdata.SourceDirRow, error) {
	// Include languages retained only for inventory: their series also feed the
	// manifest. Presence of a language alone does not prove matching input.
	expected := map[string]string{}
	for _, language := range languages {
		for _, name := range []string{"cards.json", "sets.json", "series.json"} {
			relative := language + "/" + name
			file, err := os.Open(filepath.Join(generatedDir, language, name))
			if err != nil {
				return nil, fmt.Errorf("read compiled input: %w", err)
			}
			hash := sha256.New()
			_, err = io.Copy(hash, file)
			file.Close()
			if err != nil {
				return nil, fmt.Errorf("read compiled input: %w", err)
			}
			expected[relative] = fmt.Sprintf("%x", hash.Sum(nil))
		}
	}
	seen := map[string]bool{}
	decompressed, err := gzip.NewReader(snapshot)
	if err != nil {
		return nil, fmt.Errorf("read source snapshot: %w", err)
	}
	defer decompressed.Close()

	stats := map[string]*catalogdata.SourceDirRow{}
	reader := tar.NewReader(decompressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read source snapshot: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(header.Name, "./")
		top := "."
		if index := strings.Index(name, "/"); index >= 0 {
			top = name[:index]
		}
		if stats[top] == nil {
			stats[top] = &catalogdata.SourceDirRow{Path: top}
		}
		stats[top].Files++
		stats[top].Bytes += header.Size
		_, relative, compiled := strings.Cut("/"+name, "/server/generated/")
		if want, required := expected[relative]; compiled && required {
			if seen[relative] {
				return nil, fmt.Errorf("source snapshot repeats compiled input %s", relative)
			}
			seen[relative] = true
			hash := sha256.New()
			if _, err := io.Copy(hash, reader); err != nil {
				return nil, fmt.Errorf("read compiled snapshot %s: %w", relative, err)
			}
			if fmt.Sprintf("%x", hash.Sum(nil)) != want {
				return nil, fmt.Errorf("source snapshot does not match compiled input %s", relative)
			}
		}
	}

	rows := make([]catalogdata.SourceDirRow, 0, len(stats))
	for _, row := range stats {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].Path < rows[b].Path })

	for relative := range expected {
		if !seen[relative] {
			return nil, fmt.Errorf("source snapshot is missing compiled input %s", relative)
		}
	}
	// Finish the gzip stream as well, so a damaged trailer is not accepted.
	if _, err := io.Copy(io.Discard, decompressed); err != nil {
		return nil, fmt.Errorf("read source snapshot: %w", err)
	}
	return rows, nil
}

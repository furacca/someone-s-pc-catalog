package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type buildOutput struct {
	path     string
	dir      string
	mode     os.FileMode
	previous bool
}

func (o *buildOutput) stagedPath() string { return filepath.Join(o.dir, "new") }

func (o *buildOutput) resolve(followLinks bool) error {
	// The build historically replaces the ZIP and writes through lock/report symlinks.
	if followLinks {
		resolved, err := sidecarPath(o.path)
		if err != nil {
			return err
		}
		o.path = resolved
	}
	dir, name := filepath.Split(o.path)
	if dir == "" {
		dir = "."
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("missing output filename in %s", o.path)
	}
	o.path = filepath.Join(dir, name)
	return nil
}

// A directory beside each destination keeps renames on the same filesystem.
// Its fixed name also excludes overlapping builds.
func (o *buildOutput) stage() error {
	o.dir = filepath.Join(filepath.Dir(o.path), "."+filepath.Base(o.path)+".catalog-stage")
	if err := os.Mkdir(o.dir, 0o700); err != nil {
		return fmt.Errorf("stage %s: %w", o.path, err)
	}
	info, err := os.Lstat(o.path)
	if os.IsNotExist(err) {
		o.mode = 0o644
		return nil
	}
	if err == nil && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		err = fmt.Errorf("expected a regular file at %s", o.path)
	}
	if err == nil {
		o.mode = info.Mode().Perm()
		err = os.Link(o.path, filepath.Join(o.dir, "previous"))
	}
	if err != nil {
		o.clean()
		return err
	}
	o.previous = true
	return nil
}

// Resolve the final symlink even when its target is a file this build will create.
func sidecarPath(path string) (string, error) {
	for range 40 {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return path, nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			dir, _ := filepath.Split(path)
			target = dir + target
		}
		path = target
	}
	return "", fmt.Errorf("too many symbolic links at %s", path)
}

func (o *buildOutput) write(data []byte) error {
	return os.WriteFile(o.stagedPath(), data, o.mode)
}

func (o *buildOutput) clean() {
	if o.dir != "" {
		if err := os.RemoveAll(o.dir); err != nil {
			fmt.Fprintf(os.Stderr, "build: clean staging directory: %v\n", err)
		}
	}
}

func installBuildOutputs(outputs []*buildOutput) error {
	for i, output := range outputs {
		if err := os.Rename(output.stagedPath(), output.path); err != nil {
			for _, installed := range outputs[:i] {
				if restoreErr := installed.restore(); restoreErr != nil {
					err = errors.Join(err, restoreErr)
				}
			}
			return err
		}
	}
	return nil
}

func (o *buildOutput) restore() error {
	if !o.previous {
		return os.Remove(o.path)
	}
	backup := filepath.Join(o.dir, "previous")
	if err := os.Rename(backup, o.path); err != nil {
		o.dir = "" // Keep the backup available for manual recovery.
		return fmt.Errorf("restore %s from %s: %w", o.path, backup, err)
	}
	return nil
}

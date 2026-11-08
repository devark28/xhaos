package wire

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Folders travel as tar streams: regular files and folders only (symlinks,
// devices and the rest are skipped), with their permission bits.

// WriteTar writes the files (paths relative to base, slash-separated) as a
// tar stream. Folders in the list are written as folder entries.
func WriteTar(w io.Writer, base string, files []string) error {
	tw := tar.NewWriter(w)
	for _, rel := range files {
		full := filepath.Join(base, filepath.FromSlash(rel))
		fi, err := os.Lstat(full)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // listed by git, deleted since
			}
			return err
		}
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			continue
		}
		hdr := &tar.Header{Name: rel, Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime(), Format: tar.FormatPAX}
		if fi.IsDir() {
			hdr.Typeflag, hdr.Name = tar.TypeDir, strings.TrimSuffix(rel, "/")+"/"
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			continue
		}
		hdr.Typeflag, hdr.Size = tar.TypeReg, fi.Size()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(full)
		if err != nil {
			return err
		}
		_, err = io.CopyN(tw, f, fi.Size())
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
	}
	return tw.Close()
}

// WalkFiles lists every file and folder under dir (relative, slash-separated),
// skipping .git folders. Used where there is no git to ask (on a node).
func WalkFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

// ExtractTar unpacks a tar stream into dest (created if missing). place
// maps each entry's path to where it may be written (a confinement check);
// entries that leave dest, and anything but files and folders, are refused
// or skipped. It returns the number of files written.
func ExtractTar(r io.Reader, dest string, place func(rel string) (string, error)) (int, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, err
	}
	tr := tar.NewReader(r)
	n := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return n, fmt.Errorf("tar entry %q leaves the folder", hdr.Name)
		}
		target, err := place(name)
		if err != nil {
			return n, err
		}
		mode := os.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return n, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return n, err
			}
			// Replace, never write through an existing symlink.
			_ = os.Remove(target)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode|0o600)
			if err != nil {
				return n, err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return n, err
			}
			n++
		}
	}
}

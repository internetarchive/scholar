package fileutils

import (
	"io"
	"os"
	"path/filepath"
)

// A Copier copies files.
// The operation of Copier's public functions are controled by its
// public fields. If none are set, the Copier behaves accoriding to
// the zero value rules of each public field.
type Copier struct {
}

// CopyFile copies the contents of src to dst atomically.
func (c *Copier) CopyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "copyfile")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	const perm = 0644
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// CopyFile is a convenience method that calls CopyFile on a Copier
// zero value.
func CopyFile(dst, src string) error {
	var c Copier
	return c.CopyFile(dst, src)
}

// MoveFile moves a file from src to dst atomically, even across different filesystems.
// Unlike os.Rename, this function works across device boundaries by:
// 1. Creating a temporary file in the destination directory (same filesystem)
// 2. Copying the source content to the temp file
// 3. Atomically renaming the temp file to the destination
// 4. Removing the source file
//
// This ensures the final rename is atomic within the same filesystem, avoiding
// "invalid cross-device link" errors when /tmp is on a different filesystem.
func MoveFile(dst, src string) error {
	// Open source file for reading
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Get source file info to preserve permissions
	srcInfo, err := in.Stat()
	if err != nil {
		return err
	}

	// Create temp file in destination directory (same filesystem as dst)
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-move-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	// Copy content from source to temp file
	_, err = io.Copy(tmp, in)
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}

	// Close temp file
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	// Set permissions to match source file
	if err := os.Chmod(tmpName, srcInfo.Mode()); err != nil {
		os.Remove(tmpName)
		return err
	}

	// Atomically rename temp to destination (same filesystem, so atomic)
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}

	// Remove source file only after successful rename
	if err := os.Remove(src); err != nil {
		// Destination file is already in place, but we couldn't clean up source
		// This is not a critical error, so we could log it but not fail
		return err
	}

	return nil
}

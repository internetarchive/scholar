package fileutils

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	var testroot string // unique root, populated on each test run

	// joinpath turns relative paths into paths abosolute to the test root.
	joinpath := func(args ...string) string {
		return filepath.Join(append([]string{testroot}, args...)...)
	}

	// mkdir creates a directory inside testroot
	mkdir := func(perm os.FileMode, path ...string) {
		if err := os.Mkdir(joinpath(path...), perm); err != nil {
			t.Fatal(err)
		}
	}

	// mkfile creates a file with the specified contents inside testroot
	mkfile := func(perm os.FileMode, contents string, path ...string) {
		if err := os.WriteFile(joinpath(path...), []byte(contents), perm); err != nil {
			t.Fatal(err)
		}
	}

	pass := func(t *testing.T, src, dst string, err error) {
		if err != nil {
			t.Errorf("CopyFile(%q, %q): got %v, expected %v", dst, src, err, nil)
		}
	}

	tests := []struct {
		setup    func(t *testing.T)
		dst, src string // automatically joined to testroot
		check    func(t *testing.T, src, dst string, err error)
	}{{
		setup: func(*testing.T) {
			mkdir(0755, "a")
			mkfile(0644, "file1", "a", "file1")
		},
		dst:   "a/file2",
		src:   "a/file1",
		check: pass,
	}, {
		setup: func(*testing.T) {
			mkdir(0755, "a")
			mkfile(0644, "file1", "a", "file1")
			mkfile(0644, "file2", "a", "file2")
		},
		dst:   "a/file2",
		src:   "a/file1",
		check: pass,
	}}

	// use a single tmpdir as the root of all tests to avoid spewing a million
	// tempdirs into $TMP during the test or on failure. Also, this means not
	// having to handle the cleanup of each
	root, err := os.MkdirTemp("", "testcopyfile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
	}()

	for i, tt := range tests {
		testroot, err = os.MkdirTemp(root, fmt.Sprintf("test-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		tt.setup(t)
		src := joinpath(filepath.FromSlash(tt.src))
		dst := joinpath(filepath.FromSlash(tt.dst))

		err := CopyFile(dst, src)
		tt.check(t, src, dst, err)
	}
}

func TestMoveFile(t *testing.T) {
	var testroot string // unique root, populated on each test run

	// joinpath turns relative paths into paths absolute to the test root.
	joinpath := func(args ...string) string {
		return filepath.Join(append([]string{testroot}, args...)...)
	}

	// mkdir creates a directory inside testroot
	mkdir := func(perm os.FileMode, path ...string) {
		if err := os.Mkdir(joinpath(path...), perm); err != nil {
			t.Fatal(err)
		}
	}

	// mkfile creates a file with the specified contents inside testroot
	mkfile := func(perm os.FileMode, contents string, path ...string) {
		if err := os.WriteFile(joinpath(path...), []byte(contents), perm); err != nil {
			t.Fatal(err)
		}
	}

	// fileExists checks if a file exists
	fileExists := func(path ...string) bool {
		_, err := os.Stat(joinpath(path...))
		return err == nil
	}

	// readFile reads file contents
	readFile := func(path ...string) string {
		data, err := os.ReadFile(joinpath(path...))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	// getMode gets file permissions
	getMode := func(path ...string) os.FileMode {
		info, err := os.Stat(joinpath(path...))
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode()
	}

	tests := []struct {
		name     string
		setup    func(t *testing.T)
		dst, src string // automatically joined to testroot
		check    func(t *testing.T, src, dst string, err error)
	}{
		{
			name: "basic move within same directory",
			setup: func(*testing.T) {
				mkdir(0755, "a")
				mkfile(0644, "content1", "a", "file1")
			},
			src: "a/file1",
			dst: "a/file2",
			check: func(t *testing.T, src, dst string, err error) {
				if err != nil {
					t.Errorf("MoveFile(%q, %q): unexpected error: %v", dst, src, err)
					return
				}
				// Check destination exists and has correct content
				if !fileExists("a", "file2") {
					t.Errorf("destination file does not exist")
				}
				if got := readFile("a", "file2"); got != "content1" {
					t.Errorf("destination content = %q, want %q", got, "content1")
				}
				// Check source was removed
				if fileExists("a", "file1") {
					t.Errorf("source file still exists after move")
				}
			},
		},
		{
			name: "move to different directory",
			setup: func(*testing.T) {
				mkdir(0755, "a")
				mkdir(0755, "b")
				mkfile(0644, "content2", "a", "file1")
			},
			src: "a/file1",
			dst: "b/file2",
			check: func(t *testing.T, src, dst string, err error) {
				if err != nil {
					t.Errorf("MoveFile(%q, %q): unexpected error: %v", dst, src, err)
					return
				}
				// Check destination exists in new directory
				if !fileExists("b", "file2") {
					t.Errorf("destination file does not exist in target directory")
				}
				if got := readFile("b", "file2"); got != "content2" {
					t.Errorf("destination content = %q, want %q", got, "content2")
				}
				// Check source was removed
				if fileExists("a", "file1") {
					t.Errorf("source file still exists after move")
				}
			},
		},
		{
			name: "move overwrites existing file",
			setup: func(*testing.T) {
				mkdir(0755, "a")
				mkfile(0644, "old content", "a", "file1")
				mkfile(0644, "new content", "a", "file2")
			},
			src: "a/file2",
			dst: "a/file1",
			check: func(t *testing.T, src, dst string, err error) {
				if err != nil {
					t.Errorf("MoveFile(%q, %q): unexpected error: %v", dst, src, err)
					return
				}
				// Check destination has new content
				if got := readFile("a", "file1"); got != "new content" {
					t.Errorf("destination content = %q, want %q", got, "new content")
				}
				// Check source was removed
				if fileExists("a", "file2") {
					t.Errorf("source file still exists after move")
				}
			},
		},
		{
			name: "preserves file permissions",
			setup: func(*testing.T) {
				mkdir(0755, "a")
				mkfile(0755, "executable", "a", "script.sh")
			},
			src: "a/script.sh",
			dst: "a/moved-script.sh",
			check: func(t *testing.T, src, dst string, err error) {
				if err != nil {
					t.Errorf("MoveFile(%q, %q): unexpected error: %v", dst, src, err)
					return
				}
				// Check permissions are preserved
				mode := getMode("a", "moved-script.sh")
				if mode.Perm() != 0755 {
					t.Errorf("destination permissions = %o, want %o", mode.Perm(), 0755)
				}
			},
		},
		{
			name: "error on non-existent source",
			setup: func(*testing.T) {
				mkdir(0755, "a")
			},
			src: "a/nonexistent",
			dst: "a/dest",
			check: func(t *testing.T, src, dst string, err error) {
				if err == nil {
					t.Errorf("MoveFile(%q, %q): expected error for non-existent source, got nil", dst, src)
				}
				// Destination should not be created
				if fileExists("a", "dest") {
					t.Errorf("destination file should not exist after failed move")
				}
			},
		},
		{
			name: "error on non-existent destination directory",
			setup: func(*testing.T) {
				mkdir(0755, "a")
				mkfile(0644, "content", "a", "file1")
			},
			src: "a/file1",
			dst: "b/file2",
			check: func(t *testing.T, src, dst string, err error) {
				if err == nil {
					t.Errorf("MoveFile(%q, %q): expected error for non-existent dest dir, got nil", dst, src)
				}
				// Source should still exist after failed move
				if !fileExists("a", "file1") {
					t.Errorf("source file should still exist after failed move")
				}
			},
		},
		{
			name: "handles larger files",
			setup: func(*testing.T) {
				mkdir(0755, "a")
				// Create a larger file (1MB)
				largeContent := make([]byte, 1024*1024)
				for i := range largeContent {
					largeContent[i] = byte(i % 256)
				}
				if err := os.WriteFile(joinpath("a", "large.bin"), largeContent, 0644); err != nil {
					t.Fatal(err)
				}
			},
			src: "a/large.bin",
			dst: "a/large-moved.bin",
			check: func(t *testing.T, src, dst string, err error) {
				if err != nil {
					t.Errorf("MoveFile(%q, %q): unexpected error: %v", dst, src, err)
					return
				}
				// Check destination exists
				if !fileExists("a", "large-moved.bin") {
					t.Errorf("destination file does not exist")
				}
				// Check size matches
				info, err := os.Stat(joinpath("a", "large-moved.bin"))
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() != 1024*1024 {
					t.Errorf("destination size = %d, want %d", info.Size(), 1024*1024)
				}
				// Check source was removed
				if fileExists("a", "large.bin") {
					t.Errorf("source file still exists after move")
				}
			},
		},
	}

	// use a single tmpdir as the root of all tests
	root, err := os.MkdirTemp("", "testmovefile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
	}()

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			testroot, err = os.MkdirTemp(root, fmt.Sprintf("test-%d", i))
			if err != nil {
				t.Fatal(err)
			}
			tt.setup(t)
			src := joinpath(filepath.FromSlash(tt.src))
			dst := joinpath(filepath.FromSlash(tt.dst))

			err = MoveFile(dst, src)
			tt.check(t, src, dst, err)
		})
	}
}

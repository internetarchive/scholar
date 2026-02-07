package blobproc

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/miku/blobproc/fileutils"
	"github.com/shirou/gopsutil/v3/disk"
)

const (
	DefaultURLMapHttpHeader = "X-BLOBPROC-URL"
	ExpectedSHA1Length      = 40

	tempFilePattern           = "blobprocd-*"
	defaultMinFreeDiskPercent = 10
	defaultRetryAfterSeconds  = 60
)

var errShortName = errors.New("short name")

// LimitedReader wraps an io.Reader and limits the number of bytes that can be read
type LimitedReader struct {
	R        io.Reader
	N        int64
	MaxBytes int64
}

func (l *LimitedReader) Read(p []byte) (n int, err error) {
	if l.N >= l.MaxBytes {
		return 0, fmt.Errorf("file size exceeds maximum allowed size of %d bytes", l.MaxBytes)
	}
	// Calculate how much we're allowed to read
	allowed := l.MaxBytes - l.N
	if int64(len(p)) > allowed {
		p = p[:allowed]
	}
	n, err = l.R.Read(p)
	l.N += int64(n)
	return
}

// WebSpoolService saves web payload to a configured directory. TODO: add limit
// in size (e.g. 80% of disk or absolute value)
type WebSpoolService struct {
	Dir        string
	ListenAddr string
	// TODO: add a (optional) reference to a store for url content hashes; it
	// would be good to keep it optional (so one may just copy files into the
	// spool folder), and maybe to provide a simple interface that can be
	// easily fulfilled by different backend.
	URLMap *URLMap
	// The HTTP header to look for a URL associated with a pdf blob payload.
	URLMapHttpHeader string
	// Minimum required free disk space percentage (default 10%)
	MinFreeDiskPercent int
	// Maximum allowed file size (default 0 = no limit)
	MaxFileSize int64
}

// spoolListEntry collects basic information about a spooled file.
type spoolListEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"t"`
	URL     string `json:"url"`
}

// shardedPath takes a filename (without path) and returns the full path
// including shards. If create is true, also create subdirectories, if
// necessary.
func (svc *WebSpoolService) shardedPath(filename string, create bool) (string, error) {
	if len(filename) < 8 {
		return "", errShortName
	}
	var (
		s0, s1 = filename[0:2], filename[2:4]
		dstDir = path.Join(svc.Dir, s0, s1)
	)
	if create {
		if _, err := os.Stat(dstDir); os.IsNotExist(err) {
			if err := os.MkdirAll(dstDir, 0755); err != nil {
				return "", err
			}
		}
	}
	return path.Join(dstDir, filename[4:]), nil
}

// shardedPathExists returns true, if the sharded path for a given filename exists.
func (svc *WebSpoolService) shardedPathExists(filename string) (bool, error) {
	dst, err := svc.shardedPath(filename, false)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(dst); err == nil {
		return true, nil
	}
	return false, nil
}

// shardedPathToIdentifier return the SHA1, given a sharded path.
func shardedPathToIdentifier(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return ""
	}
	n := len(parts)
	return parts[n-3] + parts[n-2] + parts[n-1]
}

// hasSufficientDiskSpace checks if there is enough free disk space in the service directory.
func (svc *WebSpoolService) hasSufficientDiskSpace() (bool, error) {
	// If MinFreeDiskPercent is not set (0), use a default
	minPercent := svc.MinFreeDiskPercent
	if minPercent <= 0 {
		minPercent = defaultMinFreeDiskPercent
	}
	usage, err := disk.Usage(svc.Dir)
	if err != nil {
		return false, err
	}
	freePercent := usage.Free * 100 / usage.Total
	return freePercent >= uint64(minPercent), nil
}

// SpoolListHandler returns a single, long jsonlines response with information
// about all files in the spool directory.
func (svc *WebSpoolService) SpoolListHandler(w http.ResponseWriter, r *http.Request) {
	var (
		entry spoolListEntry
		enc   = json.NewEncoder(w)
	)
	err := filepath.Walk(svc.Dir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		id := shardedPathToIdentifier(path)
		if len(id) == 0 {
			slog.Error("zero length id")
			w.WriteHeader(http.StatusInternalServerError)
			return fmt.Errorf("zero length id")
		}
		entry = spoolListEntry{
			Name:    id,
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
			URL:     fmt.Sprintf("http://%v/spool/%v", svc.ListenAddr, id),
		}
		if err := enc.Encode(entry); err != nil {
			slog.Error("encoding error", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return err
		}
		return nil
	})
	if err != nil {
		slog.Error("failed to list files", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// SpoolStatusHandler returns HTTP 200, if a given file is in the spool
// directory and HTTP 404, if the file is not in the spool directory.
func (svc *WebSpoolService) SpoolStatusHandler(w http.ResponseWriter, r *http.Request) {
	var (
		vars   = mux.Vars(r)
		digest = vars["id"]
	)
	if len(digest) != ExpectedSHA1Length {
		slog.Debug("invalid id", "id", digest)
		w.WriteHeader(http.StatusBadRequest)
	} else {
		ok, err := svc.shardedPathExists(digest)
		switch {
		case err != nil:
			w.WriteHeader(http.StatusInternalServerError)
		case ok:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// BlobHandler receives binary blobs and saves them on disk. This handler
// returns as soon as the file has been written into the spool directory of the
// service, using a sharded SHA1 as path.
func (svc *WebSpoolService) BlobHandler(w http.ResponseWriter, r *http.Request) {
	// Check if there's sufficient disk space before processing the request
	ok, err := svc.hasSufficientDiskSpace()
	if err != nil {
		slog.Error("failed to check disk space", "err", err, "dir", svc.Dir)
		http.Error(w, "failed to check available disk space", http.StatusInternalServerError)
		return
	}
	if !ok {
		slog.Warn("insufficient disk space, slowing down request", "dir", svc.Dir)
		// Return HTTP 429 (Too Many Requests) to signal the client to slow down
		w.Header().Set("Retry-After", fmt.Sprintf("%d", defaultRetryAfterSeconds)) // Suggest retry after 60 seconds
		http.Error(w, "insufficient disk space", http.StatusTooManyRequests)
		return
	}

	// Check for content length if provided and validate against max file size
	if svc.MaxFileSize > 0 && r.ContentLength > svc.MaxFileSize {
		slog.Warn("file too large", "size", r.ContentLength, "max", svc.MaxFileSize)
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}

	started := time.Now()
	tmpf, err := os.CreateTemp("", tempFilePattern)
	if err != nil {
		slog.Error("failed to create temporary file", "err", err)
		http.Error(w, "failed to create temporary file for upload", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpf.Name())
	var (
		h  = sha1.New()
		mw = io.MultiWriter(h, tmpf)
	)

	// Use limited reader if max file size is set
	var reader io.Reader
	if svc.MaxFileSize > 0 {
		reader = &LimitedReader{
			R:        r.Body,
			N:        0,
			MaxBytes: svc.MaxFileSize,
		}
	} else {
		reader = r.Body
	}

	n, err := io.Copy(mw, reader)
	if err != nil {
		// Check if the error is due to file size limit exceeded
		if svc.MaxFileSize > 0 && strings.Contains(err.Error(), "file size exceeds maximum allowed size") {
			slog.Warn("file size limit exceeded", "max", svc.MaxFileSize, "err", err)
			http.Error(w, fmt.Sprintf("file too large (maximum allowed: %d bytes)", svc.MaxFileSize), http.StatusRequestEntityTooLarge)
			return
		}
		slog.Error("failed to drain response body", "err", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	if err := tmpf.Close(); err != nil {
		slog.Error("failed to close temporary file", "err", err)
		http.Error(w, "failed to close temporary file", http.StatusInternalServerError)
		return
	}
	if n != r.ContentLength {
		slog.Error("content length mismatch", "n", n, "length", r.ContentLength)
		http.Error(w, "content length mismatch", http.StatusInternalServerError)
		return
	}
	var (
		digest    = fmt.Sprintf("%x", h.Sum(nil))
		spoolPath = fmt.Sprintf("/spool/%v", digest)
		spoolURL  = fmt.Sprintf("http://%v%v", svc.ListenAddr, spoolPath)
	)
	dst, err := svc.shardedPath(digest, true)
	if err != nil {
		slog.Error("could not determine sharded path", "err", err)
		http.Error(w, "failed to determine file path", http.StatusInternalServerError)
		return
	}
	ok, err = svc.shardedPathExists(digest)
	if err != nil {
		slog.Error("could not determine sharded path", "err", err)
		http.Error(w, "failed to check if file exists", http.StatusInternalServerError)
		return
	}
	if ok {
		f, err := os.Open(dst)
		if err != nil {
			slog.Error("already uploaded, but file not readable", "err", err)
			http.Error(w, "file exists but not readable", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			slog.Error("failed to stat file", "err", err)
			http.Error(w, "failed to get file stats", http.StatusInternalServerError)
			return
		}
		if r.ContentLength == fi.Size() {
			slog.Debug("found existing file in spool dir, skipping", "url", spoolURL)
			w.Header().Add("Location", spoolPath)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		slog.Debug("warning: found existing file, but size differ, overwriting")
	}
	if err := fileutils.MoveFile(dst, tmpf.Name()); err != nil {
		slog.Error("failed to move file", "err", err)
		http.Error(w, "failed to save file", http.StatusInternalServerError)
		return
	}
	// Optional: persist the URL/SHA1 pair in an sqlite3 database. If no header
	// is found or no URLMap database initialized, nothing will happen.
	curi := r.Header.Get("X-BLOBPROC-URL")
	if curi == "" {
		// TODO: Heritrix is the only client that uses this header; move
		// heritrix towards the new header.
		curi = r.Header.Get("X-Heritrix-CURI")
	}
	if curi != "" {
		slog.Debug("spooled file", "file", dst, "url", spoolURL, "t", time.Since(started), "curi", curi)
		// If we have a URLMap configured, try to record the url, sha1 pair.
		if svc.URLMap != nil {
			err := svc.URLMap.Insert(curi, digest)
			if err != nil {
				slog.Warn("could not update urlmap", "err", err, "url", curi, "sha1", digest)
			}
		}
	} else {
		slog.Debug("spooled file", "file", dst, "url", spoolURL, "t", time.Since(started))
	}
	w.Header().Add("Location", spoolURL)
	w.WriteHeader(http.StatusAccepted)
}

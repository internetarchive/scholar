package warcutil

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	warc "github.com/internetarchive/gowarc"
)

type Extracted struct {
	URI         string
	StatusCode  int
	ContentType string
	Content     io.Reader
	Size        int64
	Record      *warc.Record
}

type Processor interface {
	Process(*Extracted) error
}

type FuncProcessor func(*Extracted) error

func (f FuncProcessor) Process(ex *Extracted) error {
	return f(ex)
}

var (
	NoSharding     = func(hash string) string { return "" }
	ShardByPrefix2 = func(hash string) string {
		if len(hash) >= 2 {
			return hash[:2]
		}
		return ""
	}
)

type ResponseFilter func(resp *http.Response) bool

var PDFResponseFilter = func(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/pdf")
}

var NonZeroContentLengthFilter = func(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Length")
	l, err := strconv.Atoi(ct)
	if err != nil || l == 0 {
		return false
	}
	return true
}

var DebugProcessor = FuncProcessor(func(e *Extracted) error {
	log.Println(e.URI)
	return nil
})

// DirProcessor writes extracted files into a given directory.
type DirProcessor struct {
	Dir       string
	Prefix    string
	Extension string
}

func (d *DirProcessor) Process(ex *Extracted) error {
	f, err := os.CreateTemp(d.Dir, fmt.Sprintf("%s*%s", d.Prefix, d.Extension))
	if err != nil {
		return err
	}
	defer f.Close()

	// Only limit the reader if the size is positive, otherwise copy all content
	var reader io.Reader
	if ex.Size > 0 {
		reader = io.LimitReader(ex.Content, ex.Size)
	} else {
		reader = ex.Content
	}

	_, err = io.Copy(f, reader)
	// An EOF error from io.Copy when using io.LimitReader is expected when the limit is reached
	// and should not be treated as a failure
	if err == io.ErrUnexpectedEOF {
		log.Printf("[skip] %v got %v", ex.URI, err)
		return nil
	}
	if err != nil && err != io.EOF {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}

// HashDirProcessor writes extracted files into a given directory,
// using the hash of the content as the filename.
// Defaults to SHA1 if no HashFunc is provided.
type HashDirProcessor struct {
	Dir       string
	Extension string
	HashFunc  func() hash.Hash         // Factory function that returns a hash.Hash
	ShardFunc func(hash string) string // Returns subdirectory path; empty string means no sharding
}

func (h *HashDirProcessor) Process(ex *Extracted) error {
	hashFunc := h.HashFunc
	if hashFunc == nil {
		hashFunc = func() hash.Hash { return sha1.New() }
	}
	hasher := hashFunc()
	tf, err := os.CreateTemp(h.Dir, "temp-*")
	if err != nil {
		return err
	}
	temp := tf.Name()
	defer os.Remove(temp)
	mw := io.MultiWriter(tf, hasher)
	if _, err := io.Copy(mw, ex.Content); err != nil {
		if err == io.EOF {
			// TODO: why this fail?
			// 2025/11/07 18:10:59 copy [200] https://www.ideals.illinois.edu/items/98350/bitstreams/315024/data.pdf (9877698): unexpected EOF
			// 2025/11/07 18:10:59 unexpected EOF
			return nil
		}
		_ = tf.Close()
		return err
	}
	if err := tf.Close(); err != nil {
		return err
	}
	hexHash := hex.EncodeToString(hasher.Sum(nil))
	targetDir := h.Dir
	if h.ShardFunc != nil {
		if subdir := h.ShardFunc(hexHash); subdir != "" {
			targetDir = filepath.Join(h.Dir, subdir)
			if err := os.MkdirAll(targetDir, 0755); err != nil {
				return err
			}
		}
	}
	filename := hexHash
	if h.Extension != "" {
		filename = filename + h.Extension
	}
	dst := filepath.Join(targetDir, filename)
	return os.Rename(temp, dst)
}

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type HttpPostProcessor struct {
	URL    string
	Client Doer
}

func (h *HttpPostProcessor) Process(ex *Extracted) error {
	if h.Client == nil {
		h.Client = http.DefaultClient
	}
	buf := bytes.Buffer{}
	if _, err := io.CopyN(&buf, ex.Content, ex.Size); err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}
	req, err := http.NewRequest("POST", h.URL, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ex.ContentType)
	req.Header.Set("X-BLOBPROC-URL", ex.URI)

	resp, err := h.Client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %v", resp.StatusCode)
	}
	return nil
}

type Extractor struct {
	Filters    []ResponseFilter
	Processors []Processor
}

func (e *Extractor) Extract(r io.Reader) error {
	wr, err := warc.NewReader(r)
	if err != nil {
		return err
	}
	for {
		record, err := wr.ReadRecord()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read record: %w", err)
		}
		uri := record.Header.Get("WARC-Target-URI")
		if len(uri) == 0 {
			continue
		}
		warcContentType := record.Header.Get("Content-Type")
		if warcContentType != "application/http; msgtype=response" {
			continue
		}
		l := record.Header.Get("Content-Length")
		contentLength, err := strconv.ParseInt(l, 10, 64)
		if err != nil {
			continue
		}
		limitedReader := io.LimitReader(record.Content, contentLength)
		resp, err := http.ReadResponse(bufio.NewReader(limitedReader), nil)
		if err != nil {
			continue
		}
		defer resp.Body.Close()
		shouldProcess := true
		for _, filter := range e.Filters {
			if ok := filter(resp); !ok {
				shouldProcess = false
				break
			}
		}
		if !shouldProcess {
			_ = resp.Body.Close()
			continue
		}
		// If there's only one processor, we can use the original resp.Body directly
		if len(e.Processors) == 1 {
			extracted := &Extracted{
				URI:         uri,
				StatusCode:  resp.StatusCode,
				ContentType: resp.Header.Get("Content-Type"),
				Content:     resp.Body,
				Size:        resp.ContentLength,
				Record:      record,
			}
			for _, processor := range e.Processors {
				if err := processor.Process(extracted); err != nil {
					_ = resp.Body.Close()
					return err
				}
			}
		} else {
			// If there are multiple processors, we need to read content and make multiple copies
			// For now, read into memory with a reasonable limit (e.g., 500MB) for PDF files
			const maxContentLength = 500 * 1024 * 1024 // 500 MB limit
			if resp.ContentLength > 0 && resp.ContentLength > maxContentLength {
				log.Printf("skipping large file %s (%d bytes)", uri, resp.ContentLength)
				_ = resp.Body.Close()
				continue
			}

			contentBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxContentLength+1))
			if err != nil {
				_ = resp.Body.Close()
				continue
			}
			_ = resp.Body.Close()

			// Check if content exceeded the limit
			if int64(len(contentBytes)) > maxContentLength {
				log.Printf("skipping file %s that exceeded size limit", uri)
				continue
			}

			for _, processor := range e.Processors {
				// Create a new reader for each processor to avoid EOF issues
				extracted := &Extracted{
					URI:         uri,
					StatusCode:  resp.StatusCode,
					ContentType: resp.Header.Get("Content-Type"),
					Content:     bytes.NewReader(contentBytes),
					Size:        int64(len(contentBytes)),
					Record:      record,
				}
				if err := processor.Process(extracted); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

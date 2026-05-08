package blobproc

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miku/grobidclient"
)

// WalkStats are a poor mans metrics.
type WalkStats struct {
	Processed int64
	OK        int64
}

// SuccessRatio calculates the ration of successful to total processed files.
func (ws *WalkStats) SuccessRatio() float64 {
	if ws.Processed == 0 {
		return 1.0
	}
	return float64(ws.OK) / float64(ws.Processed)
}

// Payload is what we pass to workers. Since the worker needs file size
// information, we pass it along, as the expensive stat has already been
// performed.
type Payload struct {
	Path     string
	FileInfo fs.FileInfo
}

// WalkFast is a walker that runs postprocessing in parallel.
type WalkFast struct {
	Dir               string
	NumWorkers        int
	KeepSpool         bool
	GrobidMaxFileSize int64
	Timeout           time.Duration
	Grobid            *grobidclient.Grobid
	S3                *BlobStore
	stats             *WalkStats
}

// worker can process path from a queue in a thread. If the worker context is
// cancelled, it will wrap up the last processing step and then tear down.
func (w *WalkFast) worker(wctx context.Context, workerName string, queue chan Payload, wg *sync.WaitGroup) {
	defer wg.Done()
	logger := slog.With(
		slog.String("worker", workerName),
	)
	for payload := range queue {
		select {
		case <-wctx.Done():
			break
		default:
			wrapper := func() {
				var (
					path    = payload.Path
					started = time.Now()
				)
				logger.Debug("processing", "path", path)
				atomic.AddInt64(&w.stats.Processed, 1)
				defer func() {
					if !w.KeepSpool {
						if _, err := os.Stat(path); err == nil {
							if err := os.Remove(path); err != nil {
								logger.Warn("error removing file from spool", "err", err, "path", path)
							}
						}
					} else {
						logger.Debug("keeping file in spool", "path", path)
					}
				}()
				ctx, cancel := context.WithTimeout(wctx, w.Timeout)
				defer cancel()
				errors := ProcessPDF(ctx, ProcessPDFParams{
					Path:              path,
					Size:              payload.FileInfo.Size(),
					Grobid:            w.Grobid,
					S3:                w.S3,
					GrobidMaxFileSize: w.GrobidMaxFileSize,
					Logger:            logger,
				})
				if len(errors) == 0 {
					logger.Debug("processing finished successfully", "path", path, "t", time.Since(started), "ts", time.Since(started).Seconds())
					atomic.AddInt64(&w.stats.OK, 1)
				} else {
					logger.Warn("processing finished with some errors",
						"path", path,
						"num_errors", len(errors),
						"t", time.Since(started),
						"ts", time.Since(started).Seconds(),
					)
				}
			}
			wrapper() // for defer
		}
	}
	logger.Debug("worker shutdown ok")
}

// Run start processing files. Do some basic sanity check before setting up
// workers as we do not have a constructor function.
func (w *WalkFast) Run(ctx context.Context) error {
	if w.Grobid == nil {
		slog.Warn("GROBID client not available, GROBID processing will be skipped")
	}
	if w.S3 == nil {
		slog.Warn("S3 client not available, S3 uploads will be skipped")
	}
	w.stats = new(WalkStats)
	var queue = make(chan Payload)
	var wg sync.WaitGroup
	for i := 0; i < w.NumWorkers; i++ {
		wg.Add(1)
		name := fmt.Sprintf("worker-%02d", i)
		go w.worker(ctx, name, queue, &wg)
	}
	err := filepath.Walk(w.Dir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Size() == 0 {
			slog.Warn("skipping empty file", "path", path)
			return nil
		}
		slog.Debug("walk status", "total", w.stats.Processed, "success", w.stats.SuccessRatio())
		select {
		case queue <- Payload{Path: path, FileInfo: info}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	close(queue)
	wg.Wait()
	return err
}

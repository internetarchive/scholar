package openalex

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"git.archive.org/webgroup/scholar/trawler/s3"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// The source import runs in two halves, like the DOAJ journal import: fetching
// a day of OpenAlex's snapshot is the only part that needs the open internet,
// so it runs on the external task queue and leaves the day in S3; the import
// reads it back on the internal queue, where fatcat and ES are.
//
// OpenAlex's snapshot bucket partitions each entity by the date a record last
// changed (data/jsonl/sources/updated_date=YYYY-MM-DD/part_NNNN.gz). One run
// imports one such partition.
//
// The import is a single activity over every record rather than an activity
// per record, for the same reason as the DOAJ import: most records are one
// lookup that finds the container already there. A heartbeat checkpoint
// carries the record index, so a retried import picks up where the last
// attempt got to (see containers.ImportRows).

const (
	defaultSnapshotURL                 = "https://openalex.s3.amazonaws.com"
	defaultSourcesMaxConsecutiveErrors = 25
	snapshotBucket                     = "s3://openalex/"
	sourcesManifestPath                = "data/jsonl/sources/manifest.json"
)

// SourcesWorkflowInput drives OpenAlexSourcesWorkflow.
type SourcesWorkflowInput struct {
	// Date is the updated_date partition to import, as YYYY-MM-DD. Required
	// unless S3Key is set.
	Date string
	// SourceOverride sets the source string on created containers. By default
	// it is derived from the partition date and run.
	SourceOverride string
	// S3Key, if set, imports a day already in S3 instead of fetching it
	// (format: bucket/key).
	S3Key string
}

// sourcesConfig is the snapshot of viper-sourced settings taken via
// SideEffect, so a config change under a running workflow cannot break its
// replay.
type sourcesConfig struct {
	ExternalTaskQueue    string
	InternalTaskQueue    string
	MaxConsecutiveErrors int
}

// OpenAlexSourcesWorkflow fetches one day of OpenAlex source updates and
// creates a fatcat container for every source in it that fatcat does not
// already have.
func OpenAlexSourcesWorkflow(ctx workflow.Context, in SourcesWorkflowInput) (containers.Counts, error) {
	l := workflow.GetLogger(ctx)

	if in.Date == "" && in.S3Key == "" {
		return containers.Counts{}, temporal.NewNonRetryableApplicationError(
			"one of Date or S3Key is required", "BadInput", nil)
	}

	var cfg sourcesConfig
	if err := workflow.SideEffect(ctx, func(workflow.Context) any {
		return sourcesConfig{
			ExternalTaskQueue:    viper.GetString("openalex_sources.external_task_queue"),
			InternalTaskQueue:    viper.GetString("openalex_sources.internal_task_queue"),
			MaxConsecutiveErrors: viper.GetInt("openalex_sources.max_consecutive_errors"),
		}
	}).Get(&cfg); err != nil {
		return containers.Counts{}, fmt.Errorf("could not snapshot config: %w", err)
	}
	if cfg.MaxConsecutiveErrors <= 0 {
		cfg.MaxConsecutiveErrors = defaultSourcesMaxConsecutiveErrors
	}

	source := in.SourceOverride
	if source == "" {
		rid := workflow.GetInfo(ctx).WorkflowExecution.RunID
		if len(rid) > 8 {
			rid = rid[:8]
		}
		day := strings.ReplaceAll(in.Date, "-", "")
		if day == "" {
			day = workflow.Now(ctx).UTC().Format("20060102")
		}
		source = fmt.Sprintf("openalex-sources-%s-%s", day, rid)
	}

	s3key := in.S3Key
	if s3key == "" {
		fetchCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Hour,
			TaskQueue:           cfg.ExternalTaskQueue,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:        30 * time.Second,
				BackoffCoefficient:     2.0,
				MaximumInterval:        30 * time.Minute,
				MaximumAttempts:        6,
				NonRetryableErrorTypes: []string{"NoPartition"},
			},
		})
		var out FetchSourcesOutput
		err := workflow.ExecuteActivity(fetchCtx, FetchSourcesActivity, FetchSourcesInput{Date: in.Date}).
			Get(ctx, &out)
		if err != nil {
			return containers.Counts{}, fmt.Errorf("could not fetch openalex sources for %s: %w", in.Date, err)
		}
		s3key = out.S3Key
	}

	l.Info("importing openalex sources", "s3key", s3key, "source", source)

	importCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 8 * time.Hour,
		HeartbeatTimeout:    5 * time.Minute,
		TaskQueue:           cfg.InternalTaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Minute,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Minute,
			MaximumAttempts:    5,
		},
	})
	var counts containers.Counts
	err := workflow.ExecuteActivity(importCtx, ImportSourcesActivity, ImportSourcesInput{
		S3Key:                s3key,
		Source:               source,
		MaxConsecutiveErrors: cfg.MaxConsecutiveErrors,
	}).Get(ctx, &counts)
	if err != nil {
		return counts, fmt.Errorf("could not import openalex sources: %w", err)
	}

	l.Info("openalex source import done", "counts", counts)
	return counts, nil
}

// sourcesManifest is the subset of the snapshot's manifest.json we use. It
// lists every part file of every updated_date partition.
type sourcesManifest struct {
	Date  string `json:"date"`
	Files []struct {
		URL  string `json:"url"`
		Meta struct {
			RecordCount int `json:"record_count"`
		} `json:"meta"`
	} `json:"files"`
}

// manifestPart is one part file of a partition, as an HTTPS URL.
type manifestPart struct {
	URL         string
	RecordCount int
}

// partsForDate picks the part files of one updated_date partition out of the
// manifest, rewritten from s3:// to the bucket's public HTTPS endpoint.
func partsForDate(m sourcesManifest, snapshotURL, date string) []manifestPart {
	marker := "/updated_date=" + date + "/"
	out := []manifestPart{}
	for _, f := range m.Files {
		if !strings.Contains(f.URL, marker) {
			continue
		}
		out = append(out, manifestPart{
			URL:         strings.TrimSuffix(snapshotURL, "/") + "/" + strings.TrimPrefix(f.URL, snapshotBucket),
			RecordCount: f.Meta.RecordCount,
		})
	}
	return out
}

type FetchSourcesInput struct {
	Date string
}

type FetchSourcesOutput struct {
	S3Key string
	// Records is how many source records the day holds.
	Records int
}

// FetchSourcesActivity downloads one updated_date partition of OpenAlex's
// sources and stores it in S3 as a single gzipped JSON lines file.
//
// The manifest, not a bucket listing, says which part files make up the day:
// it is written last, so a part it names is complete, and it gives each part's
// record count. Every part is parsed and counted before anything is stored, so
// a truncated download fails here, where a retry can fix it, rather than in
// the import.
func FetchSourcesActivity(ctx context.Context, in FetchSourcesInput) (FetchSourcesOutput, error) {
	out := FetchSourcesOutput{}
	l := activity.GetLogger(ctx)

	if _, err := time.Parse(time.DateOnly, in.Date); err != nil {
		return out, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("bad date %q: %s", in.Date, err), "BadInput", nil)
	}
	snapshotURL := viper.GetString("openalex_sources.snapshot_url")
	if snapshotURL == "" {
		snapshotURL = defaultSnapshotURL
	}
	bucket := viper.GetString("openalex_sources.s3_bucket")
	if bucket == "" {
		return out, errors.New("openalex_sources.s3_bucket is not configured")
	}

	client := httpclient.Shared()

	manifestURL := strings.TrimSuffix(snapshotURL, "/") + "/" + sourcesManifestPath
	bs, err := httpclient.GetBody(ctx, client, manifestURL)
	if err != nil {
		return out, err
	}
	var m sourcesManifest
	if err := json.Unmarshal(bs, &m); err != nil {
		return out, fmt.Errorf("could not parse %s: %w", manifestURL, err)
	}

	parts := partsForDate(m, snapshotURL, in.Date)
	if len(parts) == 0 {
		// Either nothing changed that day or the snapshot does not reach it yet.
		// Retrying will not change which.
		return out, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("no sources partition for %s in the snapshot manifest (snapshot date %s)", in.Date, m.Date),
			"NoPartition", nil)
	}

	// gzip members concatenate into a valid gzip stream, so the parts are
	// stored as they came rather than recompressed.
	var day bytes.Buffer
	for _, p := range parts {
		bs, err := httpclient.GetBody(ctx, client, p.URL)
		if err != nil {
			return out, err
		}
		n, err := countSources(bs)
		if err != nil {
			return out, fmt.Errorf("%s is not usable: %w", p.URL, err)
		}
		if n != p.RecordCount {
			return out, fmt.Errorf("%s has %d records; the manifest says %d", p.URL, n, p.RecordCount)
		}
		out.Records += n
		day.Write(bs)
		activity.RecordHeartbeat(ctx)
	}

	now := time.Now().UTC()
	out.S3Key = fmt.Sprintf("%s/openalex/sources/%s/sources_%s_%s.jsonl.gz",
		bucket, in.Date[:4], in.Date, now.Format("20060102T150405Z"))

	if err := s3.PutObject(ctx, out.S3Key, day.Bytes(), "application/gzip"); err != nil {
		return out, fmt.Errorf("could not store sources at %s: %w", out.S3Key, err)
	}

	l.Info("stored openalex sources", "s3key", out.S3Key, "date", in.Date,
		"parts", len(parts), "records", out.Records, "bytes", day.Len())
	return out, nil
}

// countSources parses a gzipped part file and returns how many records it
// holds.
func countSources(gz []byte) (int, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	recs, err := parseSources(zr)
	if err != nil {
		return 0, err
	}
	return len(recs), nil
}

type ImportSourcesInput struct {
	S3Key                string
	Source               string
	MaxConsecutiveErrors int
}

// ImportSourcesActivity creates a container for each record of a stored day of
// OpenAlex sources that fatcat does not already have.
//
// See containers.ImportRows for how failures are handled.
func ImportSourcesActivity(ctx context.Context, in ImportSourcesInput) (containers.Counts, error) {
	l := activity.GetLogger(ctx)

	obj, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return containers.Counts{}, fmt.Errorf("could not open %s: %w", in.S3Key, err)
	}
	defer obj.Close()

	zr, err := gzip.NewReader(obj)
	if err != nil {
		return containers.Counts{}, fmt.Errorf("could not decompress %s: %w", in.S3Key, err)
	}
	defer zr.Close()

	recs, err := parseSources(zr)
	if err != nil {
		return containers.Counts{}, fmt.Errorf("could not parse %s: %w", in.S3Key, err)
	}

	progress := containers.ResumeProgress(ctx)

	maxConsecutive := in.MaxConsecutiveErrors
	if maxConsecutive <= 0 {
		maxConsecutive = defaultSourcesMaxConsecutiveErrors
	}

	return containers.ImportRows(ctx, l, httpclient.Shared(), asRows(recs), in.Source, maxConsecutive, progress,
		func(p containers.Progress) { activity.RecordHeartbeat(ctx, p) })
}

func asRows(recs []sourceRecord) []containers.Row {
	out := make([]containers.Row, len(recs))
	for i, r := range recs {
		out[i] = r
	}
	return out
}

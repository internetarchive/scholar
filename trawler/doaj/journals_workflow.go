package doaj

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"git.archive.org/webgroup/scholar/trawler/s3"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// The journal import runs in two halves, like the daily crawl: fetching the CSV
// is the only part that needs the open internet, so it runs on the external
// task queue and leaves the file in S3; the import reads it back on the
// internal queue, where fatcat and ES are.
//
// The import is a single activity over every row rather than an activity per
// row. A full pass is ~25k rows, nearly all of them one lookup that finds the
// journal already there; an activity each would be ~25k entries of workflow
// history to learn very little. A heartbeat checkpoint carries the row index,
// so a retried import picks up where the last attempt got to.

const (
	defaultJournalCSVURL               = "https://doaj.org/csv"
	defaultJournalMaxConsecutiveErrors = 25
)

// JournalsWorkflowInput drives DOAJJournalsWorkflow. Everything is optional;
// a scheduled run passes nothing.
type JournalsWorkflowInput struct {
	// SourceOverride sets the source string on created containers. By default
	// it is derived from the date and run.
	SourceOverride string
	// S3Key, if set, imports a CSV already in S3 instead of fetching a fresh
	// one (format: bucket/key).
	S3Key string
}

// journalsConfig is the snapshot of viper-sourced settings taken via
// SideEffect, so a config change under a running workflow cannot break its
// replay.
type journalsConfig struct {
	ExternalTaskQueue    string
	InternalTaskQueue    string
	MaxConsecutiveErrors int
}

// DOAJJournalsWorkflow fetches DOAJ's journal CSV and creates a fatcat
// container for every journal in it that fatcat does not already have.
func DOAJJournalsWorkflow(ctx workflow.Context, in JournalsWorkflowInput) (containers.Counts, error) {
	l := workflow.GetLogger(ctx)

	var cfg journalsConfig
	if err := workflow.SideEffect(ctx, func(workflow.Context) any {
		return journalsConfig{
			ExternalTaskQueue:    viper.GetString("doaj_journals.external_task_queue"),
			InternalTaskQueue:    viper.GetString("doaj_journals.internal_task_queue"),
			MaxConsecutiveErrors: viper.GetInt("doaj_journals.max_consecutive_errors"),
		}
	}).Get(&cfg); err != nil {
		return containers.Counts{}, fmt.Errorf("could not snapshot config: %w", err)
	}
	if cfg.MaxConsecutiveErrors <= 0 {
		cfg.MaxConsecutiveErrors = defaultJournalMaxConsecutiveErrors
	}

	source := in.SourceOverride
	if source == "" {
		rid := workflow.GetInfo(ctx).WorkflowExecution.RunID
		if len(rid) > 8 {
			rid = rid[:8]
		}
		source = fmt.Sprintf("doaj-journals-%s-%s", workflow.Now(ctx).UTC().Format("20060102"), rid)
	}

	s3key := in.S3Key
	if s3key == "" {
		fetchCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 15 * time.Minute,
			TaskQueue:           cfg.ExternalTaskQueue,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    30 * time.Second,
				BackoffCoefficient: 2.0,
				MaximumInterval:    30 * time.Minute,
				MaximumAttempts:    6,
			},
		})
		var out FetchJournalCSVOutput
		if err := workflow.ExecuteActivity(fetchCtx, FetchJournalCSVActivity).Get(ctx, &out); err != nil {
			return containers.Counts{}, fmt.Errorf("could not fetch doaj journal csv: %w", err)
		}
		s3key = out.S3Key
	}

	l.Info("importing doaj journals", "s3key", s3key, "source", source)

	importCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 4 * time.Hour,
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
	err := workflow.ExecuteActivity(importCtx, ImportJournalsActivity, ImportJournalsInput{
		S3Key:                s3key,
		Source:               source,
		MaxConsecutiveErrors: cfg.MaxConsecutiveErrors,
	}).Get(ctx, &counts)
	if err != nil {
		return counts, fmt.Errorf("could not import doaj journals: %w", err)
	}

	l.Info("doaj journal import done", "counts", counts)
	return counts, nil
}

type FetchJournalCSVOutput struct {
	S3Key string
}

// FetchJournalCSVActivity downloads DOAJ's journal CSV and stores it in S3.
//
// The file is parsed before it is stored, so a truncated download or an error
// page served with a 200 fails here, where a retry can fix it, rather than in
// the import.
func FetchJournalCSVActivity(ctx context.Context) (FetchJournalCSVOutput, error) {
	out := FetchJournalCSVOutput{}
	l := activity.GetLogger(ctx)

	csvURL := viper.GetString("doaj_journals.csv_url")
	if csvURL == "" {
		csvURL = defaultJournalCSVURL
	}
	bucket := viper.GetString("doaj_journals.s3_bucket")
	if bucket == "" {
		return out, errors.New("doaj_journals.s3_bucket is not configured")
	}

	// doaj.org/csv redirects to a short-lived presigned S3 URL; the shared
	// client follows it.
	bs, err := httpclient.GetBody(ctx, httpclient.Shared(), csvURL)
	if err != nil {
		return out, err
	}

	rows, err := parseJournalCSV(bytes.NewReader(bs))
	if err != nil {
		return out, fmt.Errorf("downloaded csv is not usable: %w", err)
	}

	now := time.Now().UTC()
	out.S3Key = fmt.Sprintf("%s/doaj/journals/%s/doaj_journals_%s.csv",
		bucket, now.Format("2006"), now.Format("20060102T150405Z"))

	if err := s3.PutObject(ctx, out.S3Key, bs, "text/csv"); err != nil {
		return out, fmt.Errorf("could not store csv at %s: %w", out.S3Key, err)
	}

	l.Info("stored doaj journal csv", "s3key", out.S3Key, "rows", len(rows), "bytes", len(bs))
	return out, nil
}

type ImportJournalsInput struct {
	S3Key                string
	Source               string
	MaxConsecutiveErrors int
}

// ImportJournalsActivity creates a container for each row of a stored journal
// CSV that fatcat does not already have.
//
// See containers.ImportRows for how failures are handled.
func ImportJournalsActivity(ctx context.Context, in ImportJournalsInput) (containers.Counts, error) {
	l := activity.GetLogger(ctx)

	obj, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return containers.Counts{}, fmt.Errorf("could not open %s: %w", in.S3Key, err)
	}
	defer obj.Close()

	rows, err := parseJournalCSV(obj)
	if err != nil {
		return containers.Counts{}, fmt.Errorf("could not parse %s: %w", in.S3Key, err)
	}

	progress := containers.ResumeProgress(ctx)

	maxConsecutive := in.MaxConsecutiveErrors
	if maxConsecutive <= 0 {
		maxConsecutive = defaultJournalMaxConsecutiveErrors
	}

	return containers.ImportRows(ctx, l, httpclient.Shared(), asRows(rows), in.Source, maxConsecutive, progress,
		func(p containers.Progress) { activity.RecordHeartbeat(ctx, p) })
}

func asRows(rows []journalRow) []containers.Row {
	out := make([]containers.Row, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out
}

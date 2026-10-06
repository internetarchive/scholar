package doaj

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"git.archive.org/webgroup/scholar/trawler/s3"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
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

// JournalCounts is the tally for one import.
type JournalCounts struct {
	// Rows is how many CSV rows reached a verdict.
	Rows int
	// Added is how many containers were created.
	Added int
	// AddedWithoutISSNL is how many of Added have no ISSN-L, because their
	// ISSNs are too new for the ISSN-L table. They can be found by ISSN-E or
	// ISSN-P only.
	AddedWithoutISSNL int
	// Ignored is how many rows fatcat already had a container for.
	Ignored int
	// Skipped is how many rows could not become a container (no valid ISSN, no
	// title).
	Skipped int
	// Errored is how many rows failed and were passed over. They are retried
	// on the next run, which tries every row again.
	Errored int
}

func (c JournalCounts) Add(o JournalCounts) JournalCounts {
	return JournalCounts{
		Rows:              c.Rows + o.Rows,
		Added:             c.Added + o.Added,
		AddedWithoutISSNL: c.AddedWithoutISSNL + o.AddedWithoutISSNL,
		Ignored:           c.Ignored + o.Ignored,
		Skipped:           c.Skipped + o.Skipped,
		Errored:           c.Errored + o.Errored,
	}
}

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
func DOAJJournalsWorkflow(ctx workflow.Context, in JournalsWorkflowInput) (JournalCounts, error) {
	l := workflow.GetLogger(ctx)

	var cfg journalsConfig
	if err := workflow.SideEffect(ctx, func(workflow.Context) any {
		return journalsConfig{
			ExternalTaskQueue:    viper.GetString("doaj_journals.external_task_queue"),
			InternalTaskQueue:    viper.GetString("doaj_journals.internal_task_queue"),
			MaxConsecutiveErrors: viper.GetInt("doaj_journals.max_consecutive_errors"),
		}
	}).Get(&cfg); err != nil {
		return JournalCounts{}, fmt.Errorf("could not snapshot config: %w", err)
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
			return JournalCounts{}, fmt.Errorf("could not fetch doaj journal csv: %w", err)
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
	var counts JournalCounts
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

	req, err := http.NewRequestWithContext(ctx, "GET", csvURL, nil)
	if err != nil {
		return out, fmt.Errorf("could not build request for %s: %w", csvURL, err)
	}
	req.Header.Set("User-Agent", "ia-scholar-trawler (scholar@archive.org)")

	// doaj.org/csv redirects to a short-lived presigned S3 URL; the shared
	// client follows it.
	resp, err := httpclient.Shared().Do(req)
	if err != nil {
		return out, fmt.Errorf("GET %s failed: %w", csvURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		httpclient.DrainAndClose(resp)
		return out, fmt.Errorf("GET %s returned %d", csvURL, resp.StatusCode)
	}
	bs, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return out, fmt.Errorf("could not read %s: %w", csvURL, err)
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

// importProgress is the heartbeat checkpoint: the next row to import, and the
// tally of every row before it.
type importProgress struct {
	Next   int
	Counts JournalCounts
}

// ImportJournalsActivity creates a container for each row of a stored journal
// CSV that fatcat does not already have.
//
// A row that fails is counted and passed over; one bad row must not hold up
// twenty-five thousand others, and tomorrow's run will try it again. A long
// unbroken run of failures is something else -- fatcat or ES being down -- and
// fails the activity so Temporal can retry it once things recover. The retry
// starts from the beginning of that streak, not after it.
func ImportJournalsActivity(ctx context.Context, in ImportJournalsInput) (JournalCounts, error) {
	l := activity.GetLogger(ctx)

	obj, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return JournalCounts{}, fmt.Errorf("could not open %s: %w", in.S3Key, err)
	}
	defer obj.Close()

	rows, err := parseJournalCSV(obj)
	if err != nil {
		return JournalCounts{}, fmt.Errorf("could not parse %s: %w", in.S3Key, err)
	}

	var progress importProgress
	if activity.HasHeartbeatDetails(ctx) {
		if err := activity.GetHeartbeatDetails(ctx, &progress); err != nil {
			l.Warn("could not read heartbeat checkpoint; starting from the top", "error", err)
			progress = importProgress{}
		} else {
			l.Info("resuming doaj journal import", "row", progress.Next)
		}
	}

	maxConsecutive := in.MaxConsecutiveErrors
	if maxConsecutive <= 0 {
		maxConsecutive = defaultJournalMaxConsecutiveErrors
	}

	return importJournalRows(ctx, l, httpclient.Shared(), rows, in.Source, maxConsecutive, progress,
		func(p importProgress) { activity.RecordHeartbeat(ctx, p) },
		importJournal)
}

type importFunc func(*http.Client, journalRow, string) (journalOutcome, *fatcat2.Container, error)

// importJournalRows is the loop inside ImportJournalsActivity, split out so it
// can be tested without Temporal, S3 or fatcat.
func importJournalRows(
	ctx context.Context,
	l log.Logger,
	client *http.Client,
	rows []journalRow,
	source string,
	maxConsecutive int,
	progress importProgress,
	heartbeat func(importProgress),
	importOne importFunc,
) (JournalCounts, error) {
	counts := progress.Counts

	// Where the current error streak began, and the tally at that point, so an
	// abort can checkpoint back to before it.
	streak := 0
	streakStart := progress
	for i := progress.Next; i < len(rows); i++ {
		if err := ctx.Err(); err != nil {
			return counts, err
		}

		before := counts
		outcome, c, err := importOne(client, rows[i], source)
		counts.Rows++
		if err != nil {
			if streak == 0 {
				streakStart = importProgress{Next: i, Counts: before}
			}
			streak++
			counts.Errored++
			l.Warn("doaj journal import failed", "row", i, "title", rows[i].Title, "error", err)
			if streak >= maxConsecutive {
				heartbeat(streakStart)
				return counts, fmt.Errorf("%d consecutive rows failed, last at row %d: %w", streak, i, err)
			}
		} else {
			streak = 0
			switch outcome {
			case journalAdded:
				counts.Added++
				if c != nil && c.ISSNL == "" {
					counts.AddedWithoutISSNL++
				}
				l.Info("created container", "id", c.ID, "name", c.Name,
					"issnl", c.ISSNL, "issnp", c.ISSNP, "issne", c.ISSNE)
			case journalIgnored:
				counts.Ignored++
			case journalSkipped:
				counts.Skipped++
			}
		}

		heartbeat(importProgress{Next: i + 1, Counts: counts})
	}

	return counts, nil
}

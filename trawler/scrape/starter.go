package scrape

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/spf13/viper"
	temporalsentry "github.com/uphold/temporal-sentry-interceptor"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

var unsafeIDChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// WorkflowID derives a stable ID from the list being scraped. Stability is what
// lets the conflict policy below enforce "one run per list at a time" and what
// makes the run findable later for a concurrency signal.
func WorkflowID(s3key string) string {
	slug := unsafeIDChars.ReplaceAllString(s3key, "_")
	return "scrape_to_wayback_" + strings.Trim(slug, "_")
}

// StartOneOff dispatches a scrape of a single URL list.
func StartOneOff(in ScrapeToWaybackInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	if in.S3Key == "" {
		return errors.New("an s3 key for the url list is required")
	}

	taskQueue := in.TaskQueue
	if taskQueue == "" {
		taskQueue = viper.GetString("scrape.task_queue")
		in.TaskQueue = taskQueue
	}

	workflowID := WorkflowID(in.S3Key)

	_, err = c.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: taskQueue,
			// One scrape per list at a time: FAIL rejects a start while a run is
			// in flight, so an accidental second dispatch cannot double the load
			// we put on SPN. ALLOW_DUPLICATE still permits re-running a list once
			// the previous run has closed -- worth doing after a crawler
			// improvement, since a re-run reuses fresh CDX captures and only
			// spends SPN time on URLs that are still missing.
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
			WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		},
		ScrapeToWaybackWorkflow,
		in)
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			log.Printf("scrape %s is already running; skipping", workflowID)
			return nil
		}
		return fmt.Errorf("could not start workflow %s: %w", workflowID, err)
	}

	log.Printf("dispatched %s (list=%s, concurrency=%d)", workflowID, in.S3Key, in.Concurrency)
	return nil
}

// SetConcurrency retunes a running scrape without restarting it.
func SetConcurrency(s3key string, n int) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	if n <= 0 {
		return fmt.Errorf("concurrency must be positive, got %d", n)
	}

	workflowID := WorkflowID(s3key)
	// An empty run ID targets whichever run of this workflow is currently open,
	// which is what we want: ContinueAsNew means the run ID changes as the
	// scrape progresses, but the workflow ID does not.
	if err := c.SignalWorkflow(ctx, workflowID, "", SetConcurrencySignal, n); err != nil {
		return fmt.Errorf("could not signal %s: %w", workflowID, err)
	}

	log.Printf("asked %s to run %d crawls at a time", workflowID, n)
	return nil
}

// StartWorker runs a worker for the scrape task queue.
//
// Unlike the daily crawl there is no internal/external split: every activity
// here needs the open internet, so a scrape worker belongs on a host that has
// it.
func StartWorker() error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	opts := worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{
			temporalsentry.New(),
		},
	}

	// The second throttle, and the one that actually protects SPN when several
	// workers are up: the workflow's concurrency bounds crawls in flight for the
	// run, this bounds how many any single worker will accept.
	if slots := viper.GetInt("scrape.worker_activity_slots"); slots > 0 {
		opts.MaxConcurrentActivityExecutionSize = slots
	}

	w := worker.New(c, viper.GetString("scrape.task_queue"), opts)

	w.RegisterWorkflow(ScrapeToWaybackWorkflow)
	w.RegisterActivity(ReadURLBatch)
	w.RegisterActivity(CrawlToWaybackActivity)

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("unable to start worker: %w", err)
	}

	return nil
}

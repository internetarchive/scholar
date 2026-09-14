package scrape

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/spf13/viper"
	temporalsentry "github.com/uphold/temporal-sentry-interceptor"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
)

var unsafeIDChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// scrapeWorkerStopTimeout is how long a worker being shut down waits for the
// crawls it already has before cancelling them.
//
// The SDK's default is zero, which cancels every in-flight activity the moment
// the worker is asked to stop. For this workflow that means each restart
// throws away the captures in progress and reports them as "context
// canceled" -- work that was minutes into an SPN session, abandoned to save
// seconds. Waiting is worth it. Waiting for the full StartToCloseTimeout would
// not be: that is an hour, and nobody restarts a worker on those terms.
// Stragglers past this window are cancelled and retried on another worker.
//
// The systemd unit's TimeoutStopSec has to exceed this, or systemd sends
// SIGKILL part way through the drain and the wait buys nothing.
const scrapeWorkerStopTimeout = 3 * time.Minute

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
			// A deferral is not an incident. It means SPN had no capture
			// session free, so the URL was handed back untouched for Temporal
			// to redeliver -- ordinary operation at crawl concurrency, and
			// frequent enough that reporting each one would bury the failures
			// that do want a human.
			temporalsentry.New(temporalsentry.WithFilterActivityError(
				func(err error, _ []any, _ *activity.Info) bool {
					var appErr *sdktemporal.ApplicationError
					return errors.As(err, &appErr) && appErr.Type() == errTypeSPNBusy
				})),
		},
	}

	opts.WorkerStopTimeout = scrapeWorkerStopTimeout

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
	w.RegisterActivity(RecordOutcomeActivity)

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("unable to start worker: %w", err)
	}

	return nil
}

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
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
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
// seconds. Waiting is worth it. Waiting for a batch to finish would not be:
// that is hours, and nobody restarts a worker on those terms.
//
// A batch does not have to finish for the wait to pay. It stops handing out
// URLs as soon as the worker starts shutting down (see
// activity.GetWorkerStopChannel in CrawlBatchActivity), so this window is spent
// letting the crawls already in flight reach a verdict and advance the
// checkpoint. Whatever is left is redelivered to another worker and resumes
// there.
//
// The systemd unit's TimeoutStopSec has to exceed this, or systemd sends
// SIGKILL part way through the drain and the wait buys nothing.
const scrapeWorkerStopTimeout = 3 * time.Minute

// scrapeHeartbeatThrottle is how often a batch's progress is actually written
// to Temporal, however often the batch calls RecordHeartbeat.
//
// It is the checkpoint granularity, and so the amount of crawling a power cut
// can cost: a retry resumes from the last checkpoint that reached the server.
// The SDK's default cap is a minute, which is more redone work than we need to
// accept -- a heartbeat is a single row update, and a handful of them a minute
// per in-flight batch is nothing next to the per-URL activity churn this design
// exists to avoid. Redone URLs are cheap anyway: inside the CDX freshness
// window a re-crawl is a lookup rather than a capture.
const scrapeHeartbeatThrottle = 15 * time.Second

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

// SetConcurrency retunes a running scrape without restarting it. The new value
// reaches the batches dispatched after it rather than the ones already
// running, so it takes a batch to bite.
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

	opts.WorkerStopTimeout = scrapeWorkerStopTimeout
	opts.MaxHeartbeatThrottleInterval = scrapeHeartbeatThrottle

	// How many batches a single worker will accept, not how many URLs. The
	// crawls inside a batch are bounded by the batch's own parallelism, so what
	// a worker puts on SPN is this times that -- keep this small and let the
	// run's concurrency do the tuning.
	if slots := viper.GetInt("scrape.worker_activity_slots"); slots > 0 {
		opts.MaxConcurrentActivityExecutionSize = slots
	}

	w := worker.New(c, viper.GetString("scrape.task_queue"), opts)

	w.RegisterWorkflow(ScrapeToWaybackWorkflow)
	w.RegisterActivity(PlanBatchesActivity)
	w.RegisterActivity(CrawlBatchActivity)

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("unable to start worker: %w", err)
	}

	return nil
}

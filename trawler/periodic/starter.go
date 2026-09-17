package periodic

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// periodicWorkerStopTimeout is how long a worker being shut down waits for the
// PDFs it already has before cancelling them.
//
// The SDK's default is zero, which cancels every in-flight activity the moment
// the worker is asked to stop. For this workflow that means each restart throws
// away the PDFs mid-GROBID and reports them as "context canceled" -- work that
// was minutes into a range read and a TEI extraction, abandoned to save
// seconds. Waiting is worth it. Waiting for a batch to finish would not be:
// that is hours, and nobody restarts a worker on those terms.
//
// A batch does not have to finish for the wait to pay. It stops handing out
// rows as soon as the worker starts shutting down (see
// activity.GetWorkerStopChannel in IngestBatchActivity), so this window is
// spent letting the PDFs already in flight reach a verdict and advance the
// checkpoint. Whatever is left is redelivered to another worker and resumes
// there.
//
// The systemd unit's TimeoutStopSec has to exceed this, or systemd sends
// SIGKILL part way through the drain and the wait buys nothing.
const periodicWorkerStopTimeout = 3 * time.Minute

// periodicHeartbeatThrottle is how often a batch's progress is actually written
// to Temporal, however often the batch calls RecordHeartbeat.
//
// It is the checkpoint granularity, and so the amount of ingesting a power cut
// can cost: a retry resumes from the last checkpoint that reached the server.
// The SDK's default cap is a minute, which at a minute or so per PDF is several
// PDFs of redone GROBID time. Redone rows are cheap -- every step checks
// whether its output is already there -- but not free, and a heartbeat is a
// single row update.
const periodicHeartbeatThrottle = 15 * time.Second

// WorkflowID derives a stable ID from the collection being ingested. Stability
// is what lets the conflict policy below enforce "one run per collection at a
// time" and what makes the run findable later for a concurrency signal.
func WorkflowID(collection string) string {
	slug := unsafeIDChars.ReplaceAllString(collection, "_")
	return "periodic_ingest_" + strings.Trim(slug, "_")
}

// StartCollectionIngest dispatches an ingest of a single IA crawl collection.
func StartCollectionIngest(in PeriodicIngestInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	if in.CollectionName == "" {
		return errors.New("a collection name is required")
	}

	// An offset without an item would silently apply to whatever sorts first in
	// the collection, which is never what anyone means by it.
	if in.StartAtOffset != 0 && in.StartAtItem == "" {
		return errors.New("a start-at offset needs a start-at item to be an offset into")
	}

	if in.TaskQueue == "" {
		in.TaskQueue = viper.GetString("periodic.task_queue")
	}

	workflowID := WorkflowID(in.CollectionName)

	_, err = c.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: in.TaskQueue,
			// One ingest per collection at a time: FAIL rejects a start while a
			// run is in flight, so an accidental second dispatch cannot double
			// the load we put on petabox and GROBID -- or race two runs at
			// creating the same fatcat file records. ALLOW_DUPLICATE still
			// permits re-running a collection once the previous run has closed,
			// which is how items added by later crawls get picked up: the second
			// run pins a fresh item list and skips everything already ingested.
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
			WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		},
		PeriodicIngestWorkflow,
		in)
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			log.Printf("periodic ingest %s is already running; skipping", workflowID)
			return nil
		}
		return fmt.Errorf("could not start workflow %s: %w", workflowID, err)
	}

	log.Printf("dispatched %s (collection=%s, concurrency=%d)",
		workflowID, in.CollectionName, in.Concurrency)
	return nil
}

// SetConcurrency retunes a running ingest without restarting it. The new value
// reaches the batches dispatched after it rather than the ones already running,
// so it takes a batch to bite.
func SetConcurrency(collection string, n int) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	if n <= 0 {
		return fmt.Errorf("concurrency must be positive, got %d", n)
	}

	workflowID := WorkflowID(collection)
	// An empty run ID targets whichever run of this workflow is currently open,
	// which is what we want: ContinueAsNew means the run ID changes as the
	// ingest progresses, but the workflow ID does not.
	if err := c.SignalWorkflow(ctx, workflowID, "", SetConcurrencySignal, n); err != nil {
		return fmt.Errorf("could not signal %s: %w", workflowID, err)
	}

	log.Printf("asked %s to ingest %d pdfs at a time", workflowID, n)
	return nil
}

// StartWorker runs a worker for the periodic task queue.
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
		WorkerStopTimeout:            periodicWorkerStopTimeout,
		MaxHeartbeatThrottleInterval: periodicHeartbeatThrottle,
	}

	// How many batches a single worker will accept, not how many PDFs. The rows
	// inside a batch are bounded by the batch's own parallelism, so what a
	// worker puts on GROBID is this times that -- keep this small and let the
	// run's concurrency do the tuning.
	if slots := viper.GetInt("periodic.worker_activity_slots"); slots > 0 {
		opts.MaxConcurrentActivityExecutionSize = slots
	}

	w := worker.New(c, viper.GetString("periodic.task_queue"), opts)

	w.RegisterWorkflow(PeriodicIngestWorkflow)
	w.RegisterActivity(ListCollectionActivity)
	w.RegisterActivity(PlanBatchesActivity)
	w.RegisterActivity(IngestBatchActivity)

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("unable to start worker: %w", err)
	}

	return nil
}

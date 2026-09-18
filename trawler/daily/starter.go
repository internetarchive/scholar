package daily

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

// dailyWorkerStopTimeout is how long a worker being shut down waits for the
// lines it already has before cancelling them.
//
// The SDK's default is zero, which cancels every in-flight activity the moment
// the worker is asked to stop. For this workflow that means each restart
// throws away the captures in progress and the PDFs mid-GROBID, reporting them
// as "context canceled" -- work that was minutes into an SPN session or a TEI
// extraction, abandoned to save seconds. Waiting is worth it. Waiting for a
// batch to finish would not be: that is hours, and nobody restarts a worker on
// those terms.
//
// A batch does not have to finish for the wait to pay. It stops handing out
// lines as soon as the worker starts shutting down (see
// activity.GetWorkerStopChannel in CrawlBatchActivity), so this window is spent
// letting the lines already in flight reach a verdict and advance the
// checkpoint. Whatever is left is redelivered to another worker and resumes
// there.
//
// The systemd unit's TimeoutStopSec has to exceed this, or systemd sends
// SIGKILL part way through the drain and the wait buys nothing.
const dailyWorkerStopTimeout = 3 * time.Minute

// dailyHeartbeatThrottle is how often a batch's progress is actually written
// to Temporal, however often the batch calls RecordHeartbeat.
//
// It is the checkpoint granularity, and so the amount of work a power cut can
// cost: a retry resumes from the last checkpoint that reached the server. The
// SDK's default cap is a minute, which at a crawl plus a GROBID run per
// interesting line is several minutes of redone work. Redone lines are cheap
// -- every step checks whether its output is already there -- but not free,
// and a heartbeat is a single row update.
const dailyHeartbeatThrottle = 15 * time.Second

// WorkflowID derives a stable ID from the upstream and day being crawled.
// Stability is what makes Temporal enforce "one crawl per upstream/day at a
// time" and what makes the run findable later for a concurrency signal.
func WorkflowID(upstream, day string) string {
	slug := strings.Trim(unsafeIDChars.ReplaceAllString(upstream+"_daily_"+day, "_"), "_")
	return slug
}

func StartOneOff(in DailyCrawlWorkflowInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	day, err := time.Parse("2006-01-02", in.Day)
	if err != nil {
		return fmt.Errorf("invalid day %q (want format 2006-01-02): %w", in.Day, err)
	}

	workflowID := WorkflowID(in.Upstream, day.Format("20060102"))

	_, err = c.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: viper.GetString(fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
			// A deterministic per-day ID is what makes Temporal enforce "one crawl
			// per upstream/day running at a time": FAIL rejects a start while one is
			// already running. ALLOW_DUPLICATE still lets us re-run a day once the
			// prior run has closed -- improved crawling logic or PDFs that were
			// unreachable last time can yield more files on a rerun.
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
			WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		},
		DailyCrawlWorkflow,
		in)
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			log.Printf("daily crawl %s is already running; skipping", workflowID)
			return nil
		}
		return fmt.Errorf("could not start workflow %s: %w", workflowID, err)
	}

	log.Printf("dispatched %s (concurrency=%d)", workflowID, in.Concurrency)
	return nil
}

func StartSchedule(in DailyCrawlWorkflowInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	scheduleID := fmt.Sprintf("%s_daily_schedule", in.Upstream)
	workflowID := fmt.Sprintf("%s_daily", in.Upstream)
	workflowArgs := []any{in}

	scheduleHandle, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			Calendars: []client.ScheduleCalendarSpec{{
				Hour: []client.ScheduleRange{{
					Start: 12,
				}},
			}},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        workflowID,
			Workflow:  DailyCrawlWorkflow,
			Args:      workflowArgs,
			TaskQueue: viper.GetString(fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
		},
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL,
	})
	if err != nil {
		return fmt.Errorf("could not create workflow: %w", err)
	}

	log.Printf("triggering schedule %s", scheduleID)
	err = scheduleHandle.Trigger(ctx, client.ScheduleTriggerOptions{})
	if err != nil {
		return fmt.Errorf("could not trigger schedule:%w", err)
	}

	return nil
}

// SetConcurrency retunes a running day without restarting it. The new value
// reaches the batches dispatched after it rather than the ones already
// running, so it takes a batch to bite.
//
// It takes a workflow ID rather than deriving one because a scheduled run's ID
// is not the same as a one-off's: Temporal appends the scheduled time to the
// action's ID, so the only reliable way to name the run you are looking at is
// to read it off the Temporal UI or `trawler daily one-off`'s own log line.
func SetConcurrency(workflowID string, n int) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	if workflowID == "" {
		return errors.New("a workflow id is required")
	}
	if n <= 0 {
		return fmt.Errorf("concurrency must be positive, got %d", n)
	}

	// An empty run ID targets whichever run of this workflow is currently open,
	// which is what we want: ContinueAsNew means the run ID changes as the day
	// progresses, but the workflow ID does not.
	if err := c.SignalWorkflow(ctx, workflowID, "", SetConcurrencySignal, n); err != nil {
		return fmt.Errorf("could not signal %s: %w", workflowID, err)
	}

	log.Printf("asked %s to crawl %d releases at a time", workflowID, n)
	return nil
}

type WorkerDetails struct {
	Access   string
	Upstream string
}

func StartWorker(d WorkerDetails) error {
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

	if d.Access == "internal" {
		opts.WorkerStopTimeout = dailyWorkerStopTimeout
		opts.MaxHeartbeatThrottleInterval = dailyHeartbeatThrottle

		// How many batches a single worker will accept, not how many lines. The
		// lines inside a batch are bounded by the batch's own parallelism, so
		// what a worker puts on SPN and GROBID is this times that -- keep this
		// small and let the run's concurrency do the tuning.
		if slots := viper.GetInt("daily.worker_activity_slots"); slots > 0 {
			opts.MaxConcurrentActivityExecutionSize = slots
		}
	}

	w := worker.New(c,
		viper.GetString(fmt.Sprintf("%s.%s_task_queue", d.Upstream, d.Access)),
		opts)

	if d.Access == "external" {
		w.RegisterActivity(ScholkitScrapeActivity)
	} else if d.Access == "internal" {
		w.RegisterWorkflow(DailyCrawlWorkflow)
		w.RegisterActivity(PlanBatchesActivity)
		w.RegisterActivity(CrawlBatchActivity)
	}

	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

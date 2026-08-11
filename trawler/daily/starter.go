package daily

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/spf13/viper"
	temporalsentry "github.com/uphold/temporal-sentry-interceptor"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

func StartOneOff(in DailyCrawlWorkflowInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	// Resolve the day client-side so the workflow ID names the day actually
	// crawled. Empty means "yesterday" in UTC (matching the scrape activity's day
	// boundary); pin it into the input so the workflow doesn't later compute a
	// different "yesterday" across a midnight boundary.
	if in.Day == "" {
		if in.Upstream != "doaj" {
			in.Day = time.Now().AddDate(0, 0, -1).Format("20060102")
		} else {
			// DOAJ limits non-paying consumers of their API to only records
			// updated over a month prior; if we'd otherwise crawl "yesterday" we
			// go back a month + one day.
			in.Day = time.Now().AddDate(0, 0, -32).Format("20060102")
		}
	}
	day, err := time.Parse("2006-01-02", in.Day)
	if err != nil {
		return fmt.Errorf("invalid day %q (want format 2006-01-02): %w", in.Day, err)
	}

	workflowID := fmt.Sprintf("%s_daily_%s", in.Upstream, day.Format("20060102"))

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

	log.Printf("dispatched %s", workflowID)
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
				DayOfMonth: []client.ScheduleRange{{
					Start: 1,
					End:   31,
				}},
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
	})
	if err != nil {
		return fmt.Errorf("could not create workflow: %w", err)
	}

	log.Printf("triggering schedule %s", scheduleID)
	err = scheduleHandle.Trigger(ctx, client.ScheduleTriggerOptions{
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL,
	})
	if err != nil {
		return fmt.Errorf("could not trigger schedule:%w", err)
	}

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

	w := worker.New(c,
		viper.GetString(fmt.Sprintf("%s.%s_task_queue", d.Upstream, d.Access)),
		worker.Options{
			Interceptors: []interceptor.WorkerInterceptor{
				temporalsentry.New(),
			},
		})

	if d.Access == "external" {
		w.RegisterActivity(ScholkitScrapeActivity)
	} else if d.Access == "internal" {
		w.RegisterWorkflow(DailyCrawlWorkflow)
		w.RegisterActivity(ProcessLine)
		w.RegisterActivity(harvesting.FindLineBatch)
	}

	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

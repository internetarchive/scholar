package daily

import (
	"context"
	"fmt"
	"log"

	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	temporalsentry "github.com/uphold/temporal-sentry-interceptor"
	"go.temporal.io/api/enums/v1"
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

	uid, err := uuid.NewV7()
	if err != nil {
		return err
	}

	workflowID := fmt.Sprintf("%s_daily_parent_%s", in.Upstream, uid)

	c.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: viper.GetString(fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
		},
		DailyCrawlWorkflow,
		in)

	return nil
}

func StartSchedule(in DailyCrawlWorkflowInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	every := viper.GetDuration(fmt.Sprintf("%s.every", in.Upstream))

	scheduleID := fmt.Sprintf("%s_daily_schedule", in.Upstream)

	uid, err := uuid.NewV7()
	if err != nil {
		return err
	}

	workflowID := fmt.Sprintf("%s_daily_parent_%s", in.Upstream, uid)

	workflowArgs := []any{in}

	scheduleHandle, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{
				{Every: every},
			},
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
		// TODO just guessing on this; I want re-running this to cancel the previous
		// schedule but I'm worried this means that if one invocation of the
		// scheduled activity is going that it will get cancelled when the next one
		// starts. though, maybe we want that? We could record whatever state we
		// left off in for a catch-up crawl. That way we avoid snowballing.
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_CANCEL_OTHER,
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

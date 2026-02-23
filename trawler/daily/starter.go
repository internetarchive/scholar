package daily

import (
	"context"
	"fmt"
	"log"
	"time"

	"git.archive.org/webgroup/scholar/trawler/crossref"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/pubmed"
	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func StartOneOff(in DailyCrawlWorkflowInput) error {
	// TODO start the crawl workflow manually, accept arguments from CLI
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	workflowID := fmt.Sprintf("%s_%s", in.Upstream, uuid.New())

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

	every := viper.GetString(fmt.Sprintf("%s.every", in.Upstream))
	if every == "" {
		return fmt.Errorf("%s.every needs to be set in config", in.Upstream)
	}
	duration, err := time.ParseDuration(every)
	if err != nil {
		return fmt.Errorf("could not parse %s.every: %w", in.Upstream, err)
	}

	scheduleID := fmt.Sprintf("%s_schedule_%s", in.Upstream, uuid.New())
	workflowID := fmt.Sprintf("%s_%s", in.Upstream, uuid.New())

	workflowArgs := []any{in}

	scheduleHandle, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{
				{Every: duration},
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

	w := worker.New(c, viper.GetString(fmt.Sprintf("%s.%s_task_queue", d.Upstream, d.Access)), worker.Options{})

	if d.Access == "external" {
		w.RegisterActivity(ScholkitScrapeActivity)
	} else if d.Access == "internal" {
		w.RegisterWorkflow(DailyCrawlWorkflow)
		w.RegisterWorkflow(LineBatchWorkflow)
		w.RegisterActivity(crossref.ProcessCrossrefLine)
		w.RegisterActivity(pubmed.ProcessPubmedLine)
		w.RegisterActivity(harvesting.FindLineBatch)
	}

	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

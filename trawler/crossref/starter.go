package crossref

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"storj.io/common/uuid"
)

func StartOneOff(in CrossrefCrawlInput) error {
	// TODO start the crawl workflow manually, accept arguments from CLI
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	id, err := uuid.New()
	if err != nil {
		return fmt.Errorf("could not make a workflowID: %w", err)
	}
	workflowID := "crossref_" + id.String()

	c.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: viper.GetString("crossref.internal_task_queue"),
		},
		crossrefCrawlWorkflow,
		in)

	return nil
}

func StartSchedule() error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	every := viper.GetString("crossref.every")
	if every == "" {
		return errors.New("crossref.every needs to be set in config")
	}
	duration, err := time.ParseDuration(every)
	if err != nil {
		return fmt.Errorf("could not parse crossref.every: %w", err)
	}

	id, err := uuid.New()
	if err != nil {
		return fmt.Errorf("could not make a workflowID: %w", err)
	}
	sid, err := uuid.New()
	if err != nil {
		return fmt.Errorf("could not make a workflowID: %w", err)
	}
	scheduleID := "crossref_schedule_" + sid.String()
	workflowID := "crossref_" + id.String()

	workflowArgs := []any{
		CrossrefCrawlInput{
			SKInput: SKCrossrefInput{
				Day:   "",   // Today
				Limit: 1000, // TODO for dev
			},
		},
	}

	scheduleHandle, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{
				{Every: duration},
			},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        workflowID,
			Workflow:  crossrefCrawlWorkflow,
			Args:      workflowArgs,
			TaskQueue: viper.GetString("crossref.internal_task_queue"),
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

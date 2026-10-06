package doaj

import (
	"context"
	"errors"
	"fmt"
	"log"
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

const (
	journalsScheduleID = "doaj_journals_schedule"
	journalsWorkflowID = "doaj_journals"
)

// journalsWorkerStopTimeout lets an import being shut down finish the row it is
// on and checkpoint, so the retry on another worker does not redo it. Rows are
// a few requests each; this is generous.
const journalsWorkerStopTimeout = 30 * time.Second

// StartJournalsOneOff runs a single journal import now.
func StartJournalsOneOff(in JournalsWorkflowInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	workflowID := fmt.Sprintf("%s_%s", journalsWorkflowID, time.Now().UTC().Format("20060102T150405"))

	_, err = c.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: viper.GetString("doaj_journals.internal_task_queue"),
		},
		DOAJJournalsWorkflow,
		in)
	if err != nil {
		return fmt.Errorf("could not start workflow %s: %w", workflowID, err)
	}

	log.Printf("dispatched %s", workflowID)
	return nil
}

// StartJournalsSchedule creates the daily schedule and triggers it once.
func StartJournalsSchedule(in JournalsWorkflowInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	handle, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: journalsScheduleID,
		Spec: client.ScheduleSpec{
			Calendars: []client.ScheduleCalendarSpec{{
				Hour: []client.ScheduleRange{{
					Start: viper.GetInt("doaj_journals.schedule_hour"),
				}},
			}},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        journalsWorkflowID,
			Workflow:  DOAJJournalsWorkflow,
			Args:      []any{in},
			TaskQueue: viper.GetString("doaj_journals.internal_task_queue"),
		},
		// Two imports at once would race to create the same new containers.
		// A run still going a day later has a problem a second run won't fix.
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil {
		var exists *serviceerror.AlreadyExists
		if errors.As(err, &exists) {
			return fmt.Errorf("schedule %s already exists", journalsScheduleID)
		}
		return fmt.Errorf("could not create schedule: %w", err)
	}

	log.Printf("triggering schedule %s", journalsScheduleID)
	if err := handle.Trigger(ctx, client.ScheduleTriggerOptions{}); err != nil {
		return fmt.Errorf("could not trigger schedule: %w", err)
	}

	return nil
}

// StartJournalsWorker runs a worker for one half of the journal import:
// "external" fetches the CSV and needs the internet; "internal" runs the
// workflow and the import and needs fatcat, ES and S3.
func StartJournalsWorker(access string) error {
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

	var w worker.Worker
	switch access {
	case "external":
		w = worker.New(c, viper.GetString("doaj_journals.external_task_queue"), opts)
		w.RegisterActivity(FetchJournalCSVActivity)
	case "internal":
		opts.WorkerStopTimeout = journalsWorkerStopTimeout
		w = worker.New(c, viper.GetString("doaj_journals.internal_task_queue"), opts)
		w.RegisterWorkflow(DOAJJournalsWorkflow)
		w.RegisterActivity(ImportJournalsActivity)
	default:
		return fmt.Errorf("unknown worker access %q", access)
	}

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("unable to start worker: %w", err)
	}

	return nil
}

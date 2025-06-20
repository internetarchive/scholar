package crossref

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"storj.io/common/uuid"
)

func RunStarter() error {
	c, err := client.Dial(client.Options{
		HostPort: client.DefaultHostPort,
	})
	if err != nil {
		log.Fatalln("Unable to create client", err)
	}
	defer c.Close()

	id, err := uuid.New()
	if err != nil {
		return fmt.Errorf("could not make a workflowID: %w")
	}
	workflowID := "cron_" + id.String()

	workflowOptions := client.StartWorkflowOptions{
		ID:           workflowID,
		TaskQueue:    viper.GetString("crossref.cron_task_queue"),
		CronSchedule: viper.GetString("crossref.cron"),
	}

	we, err := c.ExecuteWorkflow(context.Background(), workflowOptions, CronWorkflow)
	if err != nil {
		log.Fatalln("Unable to execute workflow", err)
	}
	log.Println("Started workflow", "WorkflowID", we.GetID(), "RunID", we.GetRunID())
	return nil
}

type CronResult struct {
	RunTime time.Time
}

func CronWorkflow(ctx workflow.Context) (*CronResult, error) {
	workflow.GetLogger(ctx).Info("Cron workflow started.", "StartTime", workflow.Now(ctx))

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	}
	ctx1 := workflow.WithActivityOptions(ctx, ao)

	// Start from 0 for first cron job
	lastRunTime := time.Time{}
	// Check to see if there was a previous cron job
	if workflow.HasLastCompletionResult(ctx) {
		var lastResult CronResult
		if err := workflow.GetLastCompletionResult(ctx, &lastResult); err == nil {
			lastRunTime = lastResult.RunTime
		}
	}
	thisRunTime := workflow.Now(ctx)

	err := workflow.ExecuteActivity(ctx1, DoSomething, lastRunTime, thisRunTime).Get(ctx, nil)
	if err != nil {
		// Cron job failed
		// Next cron will still be scheduled by the Server
		workflow.GetLogger(ctx).Error("Cron job failed.", "Error", err)
		return nil, err
	}

	return &CronResult{RunTime: thisRunTime}, nil
}

// TODO rename, lol
func DoSomething(ctx context.Context, lastRunTime, thisRunTime time.Time) error {
	activity.GetLogger(ctx).Info("Cron job running.", "lastRunTime_exclude", lastRunTime, "thisRunTime_include", thisRunTime)
	time.Sleep(time.Second * 3)
	// TODO Query database, call external API, or do any other non-deterministic action.
	return nil
}

func RunWorker() error {
	c, err := client.Dial(client.Options{
		HostPort: client.DefaultHostPort,
	})
	if err != nil {
		log.Fatalln("Unable to create client", err)
	}
	defer c.Close()

	w := worker.New(c, viper.GetString("crossref.cron_task_queue"), worker.Options{})

	w.RegisterWorkflow(CronWorkflow)
	w.RegisterActivity(DoSomething)

	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

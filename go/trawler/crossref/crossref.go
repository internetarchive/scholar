package crossref

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"
	"storj.io/common/uuid"
)

// TODO temporal connection details in config file

func ensureNamespace(ctx context.Context, namespace string) error {
	log.Printf("ensuring '%s' namespace (will create if it does not exist)...", namespace)
	client, err := client.NewNamespaceClient(client.Options{
		HostPort: client.DefaultHostPort,
	})
	if err != nil {
		return fmt.Errorf("could not create namespace client: %w", err)
	}

	// TODO is this really necessary? I see code on GitHub just passing a time.Duration instead of this durationpb.
	duration, err := time.ParseDuration(viper.GetString("crossref.temporal_namespace_retention"))
	if err != nil {
		return fmt.Errorf("could not parse crossref.temporal_namespace_retention: %w", err)
	}

	dpb := &durationpb.Duration{
		Seconds: int64(duration.Seconds()),
	}

	err = client.Register(ctx, &workflowservice.RegisterNamespaceRequest{
		Namespace:                        namespace,
		WorkflowExecutionRetentionPeriod: dpb,
	})
	var namespaceExistsError *serviceerror.NamespaceAlreadyExists
	if err != nil && !errors.As(err, &namespaceExistsError) {
		return fmt.Errorf("could not register namespace '%s': %w", namespace, err)
	}

	return nil
}

func RunStarter() error {
	ctx := context.Background()

	every := viper.GetString("crossref.every")
	if every == "" {
		return errors.New("crossref.every needs to be set in config")
	}
	duration, err := time.ParseDuration(every)
	if err != nil {
		return fmt.Errorf("could not parse crossref.every: %w", err)
	}

	namespace := viper.GetString("crossref.temporal_namespace")
	if namespace != "" {
		err := ensureNamespace(ctx, namespace)
		if err != nil {
			return fmt.Errorf("could not ensure namesapce: %w", err)
		}
	} else {
		namespace = "default"
	}

	c, err := client.Dial(client.Options{
		HostPort:  client.DefaultHostPort,
		Namespace: namespace,
	})
	if err != nil {
		log.Fatalln("Unable to create client", err)
	}
	defer c.Close()

	id, err := uuid.New()
	if err != nil {
		return fmt.Errorf("could not make a workflowID: %w")
	}
	sid, err := uuid.New()
	if err != nil {
		return fmt.Errorf("could not make a workflowID: %w")
	}
	scheduleID := "crossref_schedule_" + sid.String()
	workflowID := "crossref_" + id.String()

	scheduleHandle, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{
				{Every: duration},
			},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        workflowID,
			Workflow:  CrossrefCrawlWorkflow,
			TaskQueue: viper.GetString("crossref.task_queue"),
		},
	})
	if err != nil {
		return fmt.Errorf("could not create workflow: %w", err)
	}

	log.Printf("triggering schedule %s", scheduleID)
	err = scheduleHandle.Trigger(ctx, client.ScheduleTriggerOptions{
		// just guessing on this
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_CANCEL_OTHER,
	})

	if err != nil {
		return fmt.Errorf("could not trigger schedule:%w", err)
	}

	return nil
}

type CrossrefCrawlResult struct {
	RunTime time.Time
}

func CrossrefCrawlWorkflow(ctx workflow.Context) (*CrossrefCrawlResult, error) {
	workflow.GetLogger(ctx).Info("Cron workflow started.", "StartTime", workflow.Now(ctx))

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	}
	ctx1 := workflow.WithActivityOptions(ctx, ao)

	// Start from 0 for first cron job
	lastRunTime := time.Time{}
	// Check to see if there was a previous cron job
	if workflow.HasLastCompletionResult(ctx) {
		var lastResult CrossrefCrawlResult
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

	return &CrossrefCrawlResult{RunTime: thisRunTime}, nil
}

// TODO rename, lol
func DoSomething(ctx context.Context, lastRunTime, thisRunTime time.Time) error {
	activity.GetLogger(ctx).Info("Cron job running.", "lastRunTime_exclude", lastRunTime, "thisRunTime_include", thisRunTime)
	time.Sleep(time.Second * 3)
	// TODO Query database, call external API, or do any other non-deterministic action.
	return nil
}

func RunWorker() error {
	ctx := context.Background()
	namespace := viper.GetString("crossref.temporal_namespace")
	if namespace != "" {
		err := ensureNamespace(ctx, namespace)
		if err != nil {
			return fmt.Errorf("could not ensure namesapce: %w", err)
		}
	} else {
		namespace = "default"
	}
	c, err := client.Dial(client.Options{
		HostPort:  client.DefaultHostPort,
		Namespace: namespace,
	})
	if err != nil {
		log.Fatalln("Unable to create client", err)
	}
	defer c.Close()

	w := worker.New(c, viper.GetString("crossref.task_queue"), worker.Options{})

	w.RegisterWorkflow(CrossrefCrawlWorkflow)
	w.RegisterActivity(DoSomething)

	log.Printf("starting worker")
	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

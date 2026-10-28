package openalex

import (
	"context"
	"fmt"
	"log"
	"time"

	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/spf13/viper"
	temporalsentry "github.com/uphold/temporal-sentry-interceptor"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

const sourcesWorkflowID = "openalex_sources"

// sourcesWorkerStopTimeout lets an import being shut down finish the record it
// is on and checkpoint, so the retry on another worker does not redo it.
// Records are a few requests each; this is generous.
const sourcesWorkerStopTimeout = 30 * time.Second

// StartSourcesOneOff runs a single source import now.
func StartSourcesOneOff(in SourcesWorkflowInput) error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	workflowID := fmt.Sprintf("%s_%s", sourcesWorkflowID, time.Now().UTC().Format("20060102T150405"))
	if in.Date != "" {
		workflowID = fmt.Sprintf("%s_%s_%s", sourcesWorkflowID, in.Date, time.Now().UTC().Format("20060102T150405"))
	}

	_, err = c.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: viper.GetString("openalex_sources.internal_task_queue"),
		},
		OpenAlexSourcesWorkflow,
		in)
	if err != nil {
		return fmt.Errorf("could not start workflow %s: %w", workflowID, err)
	}

	log.Printf("dispatched %s", workflowID)
	return nil
}

// StartSourcesWorker runs a worker for one half of the source import:
// "external" fetches from OpenAlex and needs the internet; "internal" runs the
// workflow and the import and needs fatcat, ES and S3.
func StartSourcesWorker(access string) error {
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
		w = worker.New(c, viper.GetString("openalex_sources.external_task_queue"), opts)
		w.RegisterActivity(FetchSourcesActivity)
	case "internal":
		opts.WorkerStopTimeout = sourcesWorkerStopTimeout
		w = worker.New(c, viper.GetString("openalex_sources.internal_task_queue"), opts)
		w.RegisterWorkflow(OpenAlexSourcesWorkflow)
		w.RegisterActivity(ImportSourcesActivity)
	default:
		return fmt.Errorf("unknown worker access %q", access)
	}

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("unable to start worker: %w", err)
	}

	return nil
}

package periodic

import (
	"context"
	"fmt"
	"log"

	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	temporalsentry "github.com/uphold/temporal-sentry-interceptor"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

// StartCollectionIngest fires an IngestCollectionWorkflow for the given
// input and returns immediately (fire-and-forget).
func StartCollectionIngest(in IngestCollectionInput) error {
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

	workflowID := fmt.Sprintf("periodic_ingest_collection_%s", uid)

	_, err = c.ExecuteWorkflow(ctx,
		client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: viper.GetString("periodic_ingest.task_queue"),
		},
		IngestCollectionWorkflow,
		in)
	if err != nil {
		return fmt.Errorf("could not start workflow %s: %w", workflowID, err)
	}

	log.Printf("dispatched %s (collection=%s, limit=%d)", workflowID, in.CollectionID, in.Limit)
	return nil
}

func StartWorker() error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c,
		viper.GetString("periodic_ingest.task_queue"),
		worker.Options{
			Interceptors: []interceptor.WorkerInterceptor{
				temporalsentry.New(),
			},
		})

	w.RegisterWorkflow(IngestCollectionWorkflow)
	w.RegisterWorkflow(IngestItemBatchWorkflow)
	w.RegisterActivity(ListCollectionItemsActivity)
	w.RegisterActivity(FetchItemCDXActivity)
	w.RegisterActivity(ProcessItemActivity)

	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

package crossref

import (
	"context"
	"log"

	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/temporal"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/worker"
)

// StartExternalWorker starts a temporal worker that requires internet access
func StartExternalWorker() error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c, viper.GetString("crossref.external_task_queue"), worker.Options{})

	w.RegisterActivity(skCrossref)

	log.Printf("starting worker")
	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

// StartInternalWorker starts a temporal worker suitable for in-cluster activities
func StartInternalWorker() error {
	ctx := context.Background()
	c, err := temporal.SetupTemporal(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c, viper.GetString("crossref.internal_task_queue"), worker.Options{})

	w.RegisterWorkflow(crossrefCrawlWorkflow)
	w.RegisterWorkflow(lineBatchWorkflow)
	w.RegisterActivity(processLine)
	w.RegisterActivity(harvesting.FindLineBatch)
	//w.RegisterActivity(crawlForEntity)

	log.Printf("starting worker")
	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

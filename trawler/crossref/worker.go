package crossref

import (
	"context"
	"fmt"
	"log"

	"github.com/spf13/viper"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// setup creates a Temporal client, ensuring that the crossref namespace exists.
func setup() (client.Client, error) {
	ctx := context.Background()
	namespace := viper.GetString("crossref.temporal_namespace")
	if namespace != "" {
		err := ensureNamespace(ctx, namespace)
		if err != nil {
			return nil, fmt.Errorf("could not ensure namesapce: %w", err)
		}
	} else {
		namespace = "default"
	}
	hostport := viper.GetString("temporal.hostport")
	if hostport == "" {
		hostport = client.DefaultHostPort
	}

	c, err := client.Dial(client.Options{
		HostPort:  hostport,
		Namespace: namespace,
	})
	if err != nil {
		log.Fatalln("Unable to create client", err)
	}
	return c, nil
}

// StartExternalWorker starts a temporal worker that requires internet access
func StartExternalWorker() error {
	c, err := setup()
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
	c, err := setup()
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c, viper.GetString("crossref.internal_task_queue"), worker.Options{})

	w.RegisterWorkflow(crossrefCrawlWorkflow)
	w.RegisterActivity(readS3Lines)
	//w.RegisterActivity(s3ChunkToFatcat)
	//w.RegisterActivity(crawlForEntity)

	log.Printf("starting worker")
	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

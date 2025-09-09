package crossref

import (
	"context"
	"fmt"
	"log"

	"github.com/spf13/viper"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

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
	defer c.Close()

	w := worker.New(c, viper.GetString("crossref.task_queue"), worker.Options{})

	w.RegisterWorkflow(crossrefCrawlWorkflow)
	w.RegisterActivity(scholkitCrossrefDailyFeed)
	//w.RegisterActivity(chunkedS3ReadLines)
	//w.RegisterActivity(s3ChunkToFatcat)
	//w.RegisterActivity(crawlForEntity)

	log.Printf("starting worker")
	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}

	return nil
}

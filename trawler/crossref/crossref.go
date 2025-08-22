package crossref

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TODO temporal connection details in config file
// TODO defaults in config file

// notes from mike meeting:

// long running activity that marks small amount of state: like byte offset or line offset
// this makes activity resumable
// will likely need to use continueasnew to avoid history limits; invoke continueasnew once some threshold of iterations is hit
// 30k a day activity is probably fine
// can set up throughput tuning on a taskqueue -- could align this to SPN slots

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

type CrossrefCrawlResult struct {
	FoundCounts struct {
		Releases   int
		Containers int
		Creators   int
	}
	CreatedCounts struct {
		Releases   int
		Containers int
		Creators   int
	}
	PDFCount      int
	IngestedCount int
}

type entity struct {
	ID     uuid.UUID
	flavor string
}

func CrossrefCrawlWorkflow(ctx workflow.Context) (*CrossrefCrawlResult, error) {
	workflow.GetLogger(ctx).Info("CrossrefCrawlWorkflow started.", "StartTime", workflow.Now(ctx))
	out := CrossrefCrawlResult{}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 100 * time.Second,
		TaskQueue:           "scholar-external",
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var s3Key string
	err := workflow.ExecuteActivity(ctx, ScholkitCrossrefDailyFeed).Get(ctx, &s3Key)
	if err != nil {
		workflow.GetLogger(ctx).Error("ScholkitCrossrefDailyFeed failed:", err)
		return nil, err
	}

	/*
		bstart := 0
		chunkSelector := workflow.NewSelector(ctx)

		var chunkErr error
		entities := []entity{}
		fCount := 0
		for {
			result := chunkedS3Result{}
			err = workflow.ExecuteActivity(ctx, chunkedS3ReadLines, s3Key, bstart).Get(ctx, &result)
			if err != nil {
				workflow.GetLogger(ctx).Error("chunkedS3ReadLines failed:", err)
				return nil, err
			}
			// TODO process result.Lines
			if len(result.Lines) == 0 {
				break
			}
			future := workflow.ExecuteActivity(ctx, s3ChunkToFatcat, result.Lines)
			chunkSelector.AddFuture(future, func(f workflow.Future) {
				var chunkEntities []entity
				chunkErr = f.Get(ctx, &chunkEntities)
				if chunkErr != nil {
					return
				}
				for _, v := range chunkEntities {
					entities = append(entities, v)
				}
			})
			fCount++
			bstart = result.NextReadIx
		}

		crawlSelector := workflow.NewSelector(ctx)

		for x := 0; x < fCount; x++ {
			chunkSelector.Select(ctx)
			if chunkErr != nil {
				workflow.GetLogger(ctx).Error("chunk upload to fatcat failed:", chunkErr)
				return nil, err
			}
			for _, e := range entities {
				switch e.flavor {
				case "release":
					future := workflow.ExecuteActivity(ctx, crawlForEntity, e.ID)
					crawlSelector.AddFuture(future, func(f workflow.Future) {
						// TODO
						return
					})
				default:
					// TODO anything? possibly nothing if we switch to PG FTS
				}
			}
			entities = []entity{}
		}

		// TODO handle the outcome of a crawl

		// TODO what if there is one activity to read the s3 file and emit a single jsonl as input to an activity? that, in parallel, is really what I want.

		// so trying to map out how to structure the s3 read -> fatcat writes.
		// ideally, we:
		// - stream the s3 value
		// - decompress a chunk into N lines
		// - for each line, make a future for a "maybe create in fatcat" activity

		// TODO activity: read results from s3 and create in fatcat, returning fatcat IDs for paper acquisition
		// TODO activity: for each fatcat ID, attempt to acquire a paper; each of these returns an s3 key for parsing
		// TODO activity: given an s3 key for a pdf, do text extraction; returns either s3 key or the textual result of parsing
		// TODO activity: bulk ingestion into ES of parsed stuff

	*/
	return &out, nil
}

type crawlResult struct {
	// TODO
}

func crawlForEntity(ctx context.Context, entityID uuid.UUID) (crawlResult, error) {
	out := crawlResult{}
	return out, nil
}

type chunkedS3Result struct {
	NextReadIx int
	Lines      []string
}

func chunkedS3ReadLines(ctx context.Context, s3Key string, readStart, readEnd int) (chunkedS3Result, error) {
	out := chunkedS3Result{}
	return out, nil
}

func ScholkitCrossrefDailyFeed(ctx context.Context) (out string, err error) {
	// TODO eventually, if needed, this activity can take granular arguments to
	// control sk's execution
	l := activity.GetLogger(ctx)
	l.Info("ScholkitCrossrefDaily job running")

	bucket := viper.GetString("crossref.sks3bucket")
	skS3Prefix := viper.GetString("crossref.sks3prefix")

	// this is for distinguishing runs. we could tie this to the activity
	// execution if we felt like it.
	skFeedPrefix := "crossref-daily-0-"

	today := time.Now()
	yesterday := today.AddDate(0, -1, 0)
	syncEnd := today.Format("2006-01-02")
	syncStart := yesterday.Format("2006-01-02")

	skPath := viper.GetString("scholkit.path")
	skArgs := []string{
		"-s", "crossref",
		"-d", viper.GetString("scholkit.dataDir"),
		// TODO config file
		"--crossref-upload-s3",
		"--crossref-s3-rclone-remote", "seaweed314",
		"--crossref-feed-prefix", skFeedPrefix,
		"--crossref-s3-bucket", bucket,
		"--crossref-s3-prefix", skS3Prefix,
		"--crossref-sync-start", syncStart,
		"--crossref-sync-end", syncEnd,
	}
	cmd := exec.Command(skPath, skArgs...)
	// TODO may want to captue sk logging at some point
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("sk failed: %w", err)
	}

	s3Key := fmt.Sprintf("%s%s%s.ndjson",
		skS3Prefix, skFeedPrefix, syncStart)

	return s3Key, nil
}

func s3ChunkToFatcat(ctx context.Context, lines []string) ([]entity, error) {
	return []entity{}, nil
}

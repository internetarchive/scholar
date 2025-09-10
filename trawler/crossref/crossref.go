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

func crossrefCrawlWorkflow(ctx workflow.Context) (*CrossrefCrawlResult, error) {
	workflow.GetLogger(ctx).Info("CrossrefCrawlWorkflow started.", "StartTime", workflow.Now(ctx))
	out := CrossrefCrawlResult{}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 100 * time.Second,
		TaskQueue:           viper.GetString("crossref.task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	skInput := skCrossrefInput{
		Day:   "",   // today
		Limit: 1000, // TODO just for dev
	}

	var skOut skCrossrefOutput
	err := workflow.ExecuteActivity(ctx, skCrossref, skInput).Get(ctx, &skOut)
	if err != nil {
		workflow.GetLogger(ctx).Error("scholkit crossref activity failed:", err)
		return nil, err
	}
	workflow.GetLogger(ctx).Info("scholkit crossref s3key:", skOut.S3Key)

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

// TODO support args struct with: Day string, Limit int

type skCrossrefInput struct {
	// Day value in format 2006-01-02
	Day string
	// Limit of items to fetch for a day; 0 or less for unlimited
	Limit int
}

type skCrossrefOutput struct {
	// S3Key points to a large .ndjson file of metadata
	S3Key string
}

func skCrossref(ctx context.Context, in skCrossrefInput) (out skCrossrefOutput, err error) {
	// TODO eventually, if needed, this activity can take granular arguments to
	// control sk's execution (ie, run for a specific date or limit how many things to pull)
	l := activity.GetLogger(ctx)
	l.Info("starting crossref harvest")

	limit := in.Limit
	if limit == 0 {
		limit = viper.GetInt("crossref.default_limit")
	}

	s3Bucket := viper.GetString("crossref.sks3bucket")

	var syncStart time.Time
	var syncEnd time.Time
	if in.Day != "" {
		syncStart, err = time.Parse("2006-01-02", in.Day)
		if err != nil {
			return
		}
		syncEnd = syncStart.AddDate(0, 0, 1)
	} else {
		syncEnd = time.Now()
		syncStart = syncEnd.AddDate(0, 0, -1)
	}

	s3Prefix := syncEnd.Format("2006") + "/"

	// sk-feed -s crossref -d /home/nsmith/sk-test --crossref-upload-s3 --crossref-s3-rclone-remote seaweed314 --crossref-s3-bucket sk-crossref --crossref-s3-prefix 2025/ --crossref-sync-start 2025-09-08 --crossref-sync-end 2025-09-09 --limit 1000
	skPath := viper.GetString("scholkit.path")
	skArgs := []string{
		"-s", "crossref",
		"-d", viper.GetString("scholkit.dataDir"),
		"--crossref-upload-s3",
		"--crossref-s3-rclone-remote", "seaweed314",
		"--crossref-s3-bucket", s3Bucket,
		"--crossref-s3-prefix", s3Prefix,
		"--crossref-sync-start", syncStart.Format("2006-01-02"),
		"--crossref-sync-end", syncEnd.Format("2006-01-02"),
	}
	if limit > 0 {
		skArgs = append(skArgs, "--limit")
		skArgs = append(skArgs, fmt.Sprintf("%d", limit))
	}
	l.Info("cmd: ", skPath, skArgs)
	cmd := exec.Command(skPath, skArgs...)
	bs, err := cmd.Output()
	if err != nil {
		if errors.Is(err, &exec.ExitError{}) {
			l.Error("************* scholkit stderr start ****************")
			l.Error(string(err.(*exec.ExitError).Stderr))
			l.Error("************* scholkit stderr end *******#**********")
		}
		return out, fmt.Errorf("sk failed: %w", err)
	}

	l.Info("scholkit uploaded crossref data to s3 key", string(bs))

	out.S3Key = string(bs)
	return
}

func s3ChunkToFatcat(ctx context.Context, lines []string) ([]entity, error) {
	return []entity{}, nil
}

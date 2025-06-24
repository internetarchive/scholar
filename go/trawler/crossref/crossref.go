package crossref

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

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

func CrossrefCrawlWorkflow(ctx workflow.Context) (*CrossrefCrawlResult, error) {
	workflow.GetLogger(ctx).Info("CrossrefCrawlWorkflow started.", "StartTime", workflow.Now(ctx))

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 100 * time.Second,
	}
	ctx1 := workflow.WithActivityOptions(ctx, ao)

	out := CrossrefCrawlResult{}

	var s3Key string
	err := workflow.ExecuteActivity(ctx1, APIToS3).Get(ctx, &s3Key)
	if err != nil {
		workflow.GetLogger(ctx).Error("APIToS3 failed:", err)
		return nil, err
	}

	result := S3ToFatcatResult{}

	// TODO what if there is one activity to read the s3 file and emit a single jsonl as input to an activity? that, in parallel, is really what I want.

	// so trying to map out how to structure the s3 read -> fatcat writes.
	// ideally, we:
	// - stream the s3 value
	// - decompress a chunk into N lines
	// - for each line, make a future for a "maybe create in fatcat" activity
	// this is evidently possible if i use the Range feature in an s3 request to
	// get a file chunk at a time. so an activity takes a byte range and an s3
	// key and returns chunks until there are none left; for each chunk we start
	// another activity for uploading the chunks to fatcat. should be good to go.
	// just need to get going with minio.
	err = workflow.ExecuteActivity(ctx1, S3ToFatcat, s3Key).Get(ctx, &result)
	if err != nil {
		workflow.GetLogger(ctx).Error("S3ToFatcat failed:", err)
		return nil, err
	}

	// TODO activity: read results from s3 and create in fatcat, returning fatcat IDs for paper acquisition
	// TODO activity: for eatch fatcat ID, attempt to acquire a paper; each of these returns an s3 key for parsing
	// TODO activity: given an s3 key for a pdf, do text extraction; returns either s3 key or the textual result of parsing
	// TODO activity: bulk ingestion into ES of parsed stuff

	return &out, nil
}

func APIToS3(ctx context.Context) (string, error) {
	activity.GetLogger(ctx).Info("APIToS3 job running")
	// TODO scholkit
	return "", nil
}

type S3ToFatcatResult struct {
	Releases   []string
	Containers []string
	Creators   []string
	// TODO what else
}

func S3ToFatcat(ctx context.Context, s3key string) (S3ToFatcatResult, error) {
	out := S3ToFatcatResult{}
	// TODO open handle for streaming in s3key's value via zst decoder
	return out, nil
}

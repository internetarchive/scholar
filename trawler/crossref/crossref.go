package crossref

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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

// SetupTemporal creates a Temporal client, ensuring that the crossref namespace exists.
func SetupTemporal(ctx context.Context) (client.Client, error) {
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

type CrossrefCrawlInput struct {
	SKInput SKCrossrefInput
}

func crossrefCrawlWorkflow(ctx workflow.Context, in CrossrefCrawlInput) (*CrossrefCrawlResult, error) {
	workflow.GetLogger(ctx).Info("CrossrefCrawlWorkflow started.", "StartTime", workflow.Now(ctx))
	out := CrossrefCrawlResult{}

	// fetch crossref metadata from the upstream API and store in s3

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString("crossref.external_task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var skOut skCrossrefOutput
	err := workflow.ExecuteActivity(ctx, skCrossref, in.SKInput).Get(ctx, &skOut)
	if err != nil {
		workflow.GetLogger(ctx).Error("scholkit crossref activity failed:", err)
		return nil, err
	}
	workflow.GetLogger(ctx).Info("scholkit crossref s3key:", skOut.S3Key)

	// read chunks of ndjson and process

	ao.TaskQueue = viper.GetString("crossref.internal_task_queue")
	// TODO ok to reuse ctx for this? Should I be creating from the initial ctx before the last WithActivityOptions?
	ctx = workflow.WithActivityOptions(ctx, ao)

	//chunkSelector := workflow.NewSelector(ctx)
	//var chunkErr error
	//entities := []entity{}
	//fCount := 0
	readS3In := readS3LinesInput{
		S3Key:     skOut.S3Key,
		ReadStart: 0,
		ChunkSize: 1024 * 20,
	}

	l := workflow.GetLogger(ctx)

	for {
		out := readS3LinesOutput{}
		err = workflow.ExecuteActivity(ctx, readS3Lines, readS3In).Get(ctx, &out)
		if err != nil {
			l.Error("readS3Lines failed:", err)
			return nil, err
		}
		l.Info(fmt.Sprintf("read %d lines from %s", len(out.Lines), skOut.S3Key))
		if len(out.Lines) == 0 {
			break
		}
		// TODO do stuff with out.Lines
		readS3In.ReadStart = out.NextReadIx
	}

	/*

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

type readS3LinesOutput struct {
	NextReadIx int64
	Lines      []string
}

type readS3LinesInput struct {
	S3Key     string
	ReadStart int64
	ChunkSize int64
}

func readS3Lines(ctx context.Context, in readS3LinesInput) (out readS3LinesOutput, err error) {
	out = readS3LinesOutput{
		Lines: []string{},
	}
	endpoint := viper.GetString("s3.endpoint")
	accessKeyID := viper.GetString("s3.access_id")
	secretAccessKey := viper.GetString("s3.secret_key")
	// useSSL := true
	useSSL := false // thonk

	// Initialize minio client object.
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return
	}

	sp := strings.SplitN(in.S3Key, "/", 2)
	if len(sp) != 2 {
		return out, errors.New("could not parse s3 key " + in.S3Key)
	}
	bucket := sp[0]
	key := sp[1]

	l := activity.GetLogger(ctx)
	l.Info(fmt.Sprintf("doing a range read from bucket '%s', key '%s'", bucket, key))

	opts := minio.GetObjectOptions{}
	// NB it seems that you can _either_ use SetRange _or_ ReadAt. I'm going with ReadAt arbitrarily.
	//opts.SetRange(in.ReadStart, in.ReadStart+in.ChunkSize)
	f, err := mc.GetObject(ctx, bucket, key, opts)
	if err != nil {
		return out, fmt.Errorf("GetObject failed: %w", err)
	}
	defer f.Close()

	// TODO refactor this so it's unit testable
	b := make([]byte, in.ChunkSize)

	n, err := f.ReadAt(b, in.ReadStart)
	if err != nil && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("range read of '%s' failed: %w", in.S3Key, err)
	} else {
		err = nil
	}

	l.Debug("READ SOME STUFF MAYBE?")
	l.Debug(fmt.Sprintf("%d", n))

	if n == 0 {
		return
	}

	lineBuf := []byte{}
	var lastNewlineIx int64
	for x := range len(b) {
		if b[x] == '\n' {
			lastNewlineIx = int64(x)
			out.Lines = append(out.Lines, string(lineBuf))
			lineBuf = []byte{}
			continue
		}

		lineBuf = append(lineBuf, b[x])
	}

	out.NextReadIx = in.ReadStart + lastNewlineIx

	return
}

type SKCrossrefInput struct {
	// Day value in format 2006-01-02
	Day string
	// Limit of items to fetch for a day; 0 or less for unlimited
	Limit int
}

type skCrossrefOutput struct {
	// S3Key points to a large .ndjson file of metadata
	S3Key string
}

func skCrossref(ctx context.Context, in SKCrossrefInput) (out skCrossrefOutput, err error) {
	// TODO eventually, if needed, this activity can take granular arguments to
	// control sk's execution (ie, run for a specific date or limit how many things to pull)
	l := activity.GetLogger(ctx)
	l.Info("starting crossref harvest", in)

	limit := in.Limit
	if limit == 0 {
		limit = viper.GetInt("crossref.default_limit")
	}

	s3Bucket := viper.GetString("crossref.sks3bucket")

	// TODO this is going to cause a problem with heartbeating; sk-feed might run
	// for hours.
	// Need to have a goroutine sending heartbeats while sk-feed is running

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

	out.S3Key = strings.TrimSpace(string(bs))
	return
}

func s3ChunkToFatcat(ctx context.Context, lines []string) ([]entity, error) {
	return []entity{}, nil
}

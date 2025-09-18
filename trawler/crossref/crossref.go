package crossref

import (
	"context"
	"encoding/json"
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
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"
)

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

// scrape crossref for a day's worth of metadata: huge ndjson file in s3, each line a paper-like entity
// for each entity, create an entry in fatcat2
// for each entity, if there's a suitable URL, try and obtain a PDF
// for each obtained PDF
// - extract metadata and make sure it matches the fatcat2 record
// - create a file entry in fatcat2
// - extract fulltext and ingest into elasticsearch

// TODO uhhh do i want this actually
/*
type chunkParseWorkflowInput struct {
	Offset  int64
	Partial []byte
}

type chunkParseWorkflowOutput struct {
	// TODO
}

func chunkParseWorkflow(ctx workflow.Context, in chunkParseWorkflowInput) (*CrossrefCrawlResult, error) {

	return nil, nil
}
*/

type lineBatchInput struct {
	// S3Key is a key to a .ndjson file in s3 storage
	S3Key string
	// Offsets is a list of pairs of [ReadOffset, Length]
	Offsets [][]int64
}

type counts struct {
	// Ignored is the count of lines in the upstream metadata we passed on
	Ignored int
	// Added is the count of lines from the upstream metadata we added to Fatcat
	Added int
	// Acquired is the count of PDFs we acquired from the upstream metadata
	Acquired int
}

func lineBatchWorkflow(ctx workflow.Context, in lineBatchInput) (counts, error) {
	out := counts{}
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,                       // TODO tune, config maybe
		TaskQueue:           viper.GetString("crossref.internal_task_queue"), // TODO needed?
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	lin := lineInput{
		S3Key: in.S3Key,
	}
	for _, offset := range in.Offsets {
		lin.LineStart = offset[0]
		lin.Length = offset[1]

		var c counts

		err := workflow.ExecuteActivity(ctx, processLine, lin).Get(ctx, &c)
		if err != nil {
			return out, err
		}
		out.Ignored += c.Ignored
		out.Added += c.Added
		out.Acquired += c.Acquired
	}
	return out, nil
}

type lineInput struct {
	S3Key     string
	LineStart int64
	Length    int64
}

func processLine(ctx context.Context, in lineInput) (out counts, err error) {
	out = counts{}
	f, err := getS3Object(ctx, in.S3Key)
	if err != nil {
		return
	}
	defer f.Close()

	lineb := make([]byte, in.Length)
	n, err := f.ReadAt(lineb, in.LineStart)
	if err != nil {
		return
	}
	if n == 0 {
		return out, fmt.Errorf("read 0 bytes, expected %d", len(lineb))
	}

	l := activity.GetLogger(ctx)
	l.Debug(string(lineb))

	// TODO design struct
	type crossrefDoc struct {
		Type string
		DOI  string
	}
	var parsed crossrefDoc

	err = json.Unmarshal(lineb, &parsed)
	if err != nil {
		return
	}

	l.Info(fmt.Sprintf("got a '%s' with doi '%s'", parsed.Type, parsed.DOI))

	// TODO do things
	return out, nil
}

type findLineBatchInput struct {
	S3Key  string
	Offset int64
}

type findLineBatchOutput struct {
	Offsets   [][]int64
	BytesRead int64
	EOF       bool
}

func findLineBatch(ctx context.Context, in findLineBatchInput) (out findLineBatchOutput, err error) {
	out = findLineBatchOutput{}

	l := activity.GetLogger(ctx)
	l.Info(fmt.Sprintf("doing a range read from '%s'", in.S3Key))

	f, err := getS3Object(ctx, in.S3Key)
	if err != nil {
		return
	}
	defer f.Close()

	batchSize := 1000 // TODO set in config

	// TODO refactor this so it's unit testable
	chunkSize := 1024 * 100 // TODO set in config
	out.BytesRead = in.Offset
	curLineStart := in.Offset

	var done bool
	var curLineLength int64

	for !done {
		b := make([]byte, chunkSize)
		n, err := f.ReadAt(b, out.BytesRead)
		l.Debug(fmt.Sprintf("read %d bytes", n))
		if errors.Is(err, io.EOF) {
			l.Debug("saw EOF")
			out.EOF = true
			err = nil
		}
		if err != nil {
			return out, fmt.Errorf("range read of '%s' failed: %w", in.S3Key, err)
		}
		if n == 0 {
			return out, nil
		}
		for x := range n {
			out.BytesRead++
			curLineLength++
			if b[x] == '\n' {
				out.Offsets = append(out.Offsets, []int64{curLineStart, curLineLength})
				if len(out.Offsets) == batchSize {
					done = true
					break
				}
				curLineStart = out.BytesRead
				curLineLength = 0
			}
		}

		if out.EOF {
			done = true
		}
	}

	return
}

func crossrefCrawlWorkflow(ctx workflow.Context, in CrossrefCrawlInput) (counts, error) {
	workflow.GetLogger(ctx).Info("CrossrefCrawlWorkflow started.", "StartTime", workflow.Now(ctx))
	out := counts{}

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
		return out, err
	}
	workflow.GetLogger(ctx).Info("scholkit crossref s3key:", skOut.S3Key)

	ao = workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString("crossref.internal_task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	batchInput := lineBatchInput{
		S3Key: skOut.S3Key,
	}
	findInput := findLineBatchInput{
		S3Key: skOut.S3Key,
	}
	findOutput := findLineBatchOutput{}
	childSelector := workflow.NewSelector(ctx)
	var childCount int

	var childErr error
	var childCounts counts
	for {
		err := workflow.ExecuteActivity(ctx, findLineBatch, findInput).Get(ctx, &findOutput)
		if err != nil {
			return out, err
		}
		if len(findOutput.Offsets) > 0 {
			findInput.Offset = findOutput.BytesRead
			batchInput.Offsets = findOutput.Offsets
			childWorkflowOptions := workflow.ChildWorkflowOptions{
				ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON,
			}
			ctx = workflow.WithChildOptions(ctx, childWorkflowOptions)
			fut := workflow.ExecuteChildWorkflow(ctx, lineBatchWorkflow, batchInput)
			var cwe workflow.Execution
			err := fut.GetChildWorkflowExecution().Get(ctx, &cwe)
			if err != nil {
				return out, err
			}
			childSelector.AddFuture(fut, func(f workflow.Future) {
				childErr = f.Get(ctx, &childCounts)
			})
			childCount++
		}
		if findOutput.EOF {
			break
		}
	}

	for range childCount {
		childSelector.Select(ctx)
		if childErr != nil {
			return out, err
		}
		out.Added += childCounts.Added
		out.Ignored += childCounts.Ignored
		out.Acquired += childCounts.Acquired
	}

	return out, nil
	/*

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

			// TODO activity: for each fatcat ID, attempt to acquire a paper; each of these returns an s3 key for parsing
			// TODO activity: given an s3 key for a pdf, do text extraction; returns either s3 key or the textual result of parsing
			// TODO activity: bulk ingestion into ES of parsed stuff

		return &out, nil
	*/
}

type crawlResult struct {
	// TODO
}

func crawlForEntity(ctx context.Context, entityID uuid.UUID) (crawlResult, error) {
	out := crawlResult{}
	return out, nil
}

func getS3Object(ctx context.Context, s3key string) (*minio.Object, error) {
	endpoint := viper.GetString("s3.endpoint")
	accessKeyID := viper.GetString("s3.access_id")
	secretAccessKey := viper.GetString("s3.secret_key")
	// useSSL := true
	useSSL := false // thonk
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	sp := strings.SplitN(s3key, "/", 2)
	if len(sp) != 2 {
		return nil, errors.New("could not parse s3 key " + s3key)
	}
	bucket := sp[0]
	key := sp[1]

	opts := minio.GetObjectOptions{}
	f, err := mc.GetObject(ctx, bucket, key, opts)
	if err != nil {
		return nil, fmt.Errorf("GetObject failed: %w", err)
	}
	return f, nil
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

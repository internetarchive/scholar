package crossref

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

// scrape crossref for a day's worth of metadata: huge ndjson file in s3, each line a paper-like entity
// for each entity, create an entry in fatcat2
// for each entity, if there's a suitable URL, try and obtain a PDF
// for each obtained PDF
// - extract metadata and make sure it matches the fatcat2 record
// - create a file entry in fatcat2
// - extract fulltext and ingest into elasticsearch

// TODO split out into its own package
type Release struct {
	Title string
	// TODO based on djscholar model
}

// TODO design struct
type crossrefDoc struct {
	Title []string
	Type  string
	DOI   string
}

var ignoredTypes = []string{
	"",
	"journal",
	"proceedings",
	"standard-series",
	"report-series",
	"book-series",
	"book-set",
	"book-track",
	"proceedings-series",
}

func (c crossrefDoc) IsSkippable() bool {
	if c.Title == nil {
		return true
	}

	if len(c.Title) < 1 {
		return true
	}

	if strings.ToLower(c.Title[0]) == "oup accepted manuscript" {
		return true
	}

	if slices.Contains(ignoredTypes, c.Type) {
		return true
	}

	if c.DOI == "" {
		return true
	}

	return false
}

/*
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
*/

type CrossrefCrawlInput struct {
	SKInput SKCrossrefInput
}

type counts struct {
	// Skipped is the count of lines in the upstream we knew we would never want
	Skipped int
	// Ignored is the count of lines in the upstream metadata we were already aware of
	Ignored int
	// Added is the count of lines from the upstream metadata we added to Fatcat
	Added int
	// Acquired is the count of PDFs we acquired from the upstream metadata
	Acquired int
}

type lineBatchInput struct {
	// S3Key is a key to a .ndjson file in s3 storage
	S3Key string
	// Offsets is a list of pairs of [ReadOffset, Length]
	Offsets [][]int64
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
	//l.Debug(string(lineb))

	var parsed crossrefDoc

	err = json.Unmarshal(lineb, &parsed)
	if err != nil {
		return
	}

	l.Info(fmt.Sprintf("got a '%s' with doi '%s'", parsed.Type, parsed.DOI))

	// Should we skip even checking the DOI?

	if parsed.IsSkippable() {
		out.Skipped++
		return out, nil
	}

	// Check the DOI

	client := &http.Client{}
	// TODO http client for fatcat2
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/release/lookup", nil)
	if err != nil {
		panic(err)
	}
	q := req.URL.Query()
	q.Add("id_type", "doi")
	q.Add("id_value", parsed.DOI)
	req.URL.RawQuery = q.Encode()
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("fc2 lookup failed for '%s': %w", parsed.DOI, err)
	}

	if resp.StatusCode == http.StatusOK {
		// We already know about this DOI; bail. In the future we may attempt updates.
		out.Ignored++
		return out, nil
	}

	if resp.StatusCode != 404 {
		return out, fmt.Errorf("did not get 200 nor 404 from fc2 api: %d", resp.StatusCode)
	}

	// TODO create entity for insertion
	// TODO insert into db (Added++)
	// TODO wait for spn slot
	// TODO submit to spn
	// TODO fetch pdf from wayback
	// TODO store pdf in seaweed
	// TODO extract and check PDF metadata
	// TODO insert file into db
	// TODO ingest PDF (Acquired++)
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
	l := workflow.GetLogger(ctx)
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
		l.Info(fmt.Sprintf("child ignored %d lines", childCounts.Ignored))
	}

	l.Info(fmt.Sprintf("found and ignored %d lines", out.Ignored))

	return out, nil

	// TODO handle the outcome of a crawl

	// TODO activity: for each fatcat ID, attempt to acquire a paper; each of these returns an s3 key for parsing
	// TODO activity: given an s3 key for a pdf, do text extraction; returns either s3 key or the textual result of parsing
	// TODO activity: bulk ingestion into ES of parsed stuff
}

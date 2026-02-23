package daily

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/crossref"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/pubmed"
	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

type DailyCrawlWorkflowInput struct {
	// Day value in format 2006-01-02
	Day string
	// Limit of items to fetch for a day; 0 or less for unlimited
	Limit int
	// SourceOverride sets a static string to use instead of letting trawler
	// generate a source value dynamically
	SourceOverride string
	// Upstream is which API we're scraping
	Upstream string
}

func DailyCrawlWorkflow(ctx workflow.Context, in DailyCrawlWorkflowInput) (counts.Counts, error) {
	l := workflow.GetLogger(ctx)
	out := counts.Counts{}
	source := in.SourceOverride
	if source == "" {
		day := in.Day
		if day == "" {
			day = workflow.Now(ctx).AddDate(0, 0, -1).Format("2006-01-02")
		}
		rid := workflow.GetInfo(ctx).WorkflowExecution.RunID
		if len(rid) > 8 {
			rid = rid[:8]
		}
		source = fmt.Sprintf("%s-%s-%s", in.Upstream, day, rid)
	}

	// fetch metadata from the upstream API and store in s3

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString(fmt.Sprintf("%s.external_task_queue", in.Upstream)),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	scrapeIn := scholkitScrapeInput{
		Day:      in.Day,
		Limit:    in.Limit,
		Upstream: in.Upstream,
	}
	var scrapeOut scholkitScrapeOutput
	err := workflow.ExecuteActivity(ctx, ScholkitScrapeActivity, scrapeIn).Get(ctx, &scrapeOut)
	if err != nil {
		l.Error(fmt.Sprintf("scholkit %s activity failed: %s", in.Upstream, err))
		return out, err
	}
	l.Info(fmt.Sprintf("scholkit %s s3key: %s", in.Upstream, scrapeOut.S3Key))

	// process the harvested data

	ao = workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString(fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	batchInput := lineBatchInput{
		S3Key:    scrapeOut.S3Key,
		Source:   source,
		Upstream: in.Upstream,
	}
	findInput := harvesting.FindLineBatchInput{
		S3Key: scrapeOut.S3Key,
	}
	findOutput := harvesting.FindLineBatchOutput{}
	childSelector := workflow.NewSelector(ctx)
	var childCount int

	var childErr error
	var childCounts counts.Counts
	for {
		err := workflow.ExecuteActivity(ctx, harvesting.FindLineBatch, findInput).Get(ctx, &findOutput)
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
			fut := workflow.ExecuteChildWorkflow(ctx, LineBatchWorkflow, batchInput)
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
			return out, childErr
		}
		out = out.Add(childCounts)
		l.Info(fmt.Sprintf("child ignored %d lines", childCounts.Releases.Ignored))
	}

	l.Info(fmt.Sprintf("%#v", out))

	return out, nil
}

type lineBatchInput struct {
	// S3Key is a key to a .ndjson file in s3 storage
	S3Key string
	// Offsets is a list of pairs of [ReadOffset, Length]
	Offsets [][]int64
	// Source identifies what crawl led to the creation of records for provenance purposes
	Source string
	// Upstream is which API we're scraping
	Upstream string
}

func LineBatchWorkflow(ctx workflow.Context, in lineBatchInput) (counts.Counts, error) {
	out := counts.Counts{}
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second, // TODO tune, config maybe
		TaskQueue: viper.GetString(
			fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	lin := harvesting.ProcessLineInput{
		S3Key:  in.S3Key,
		Source: in.Source,
	}
	for _, offset := range in.Offsets {
		lin.LineStart = offset[0]
		lin.Length = offset[1]

		var c counts.Counts

		// TODO can we afford two or three activities per line? if we can, i'd rather see:
		// - harvestUpstream
		// - crawl
		// - handlePDF
		// but for now i'll keep it one per line

		var processLine func(context.Context, harvesting.ProcessLineInput) (counts.Counts, error)
		switch in.Upstream {
		case "crossref":
			processLine = crossref.ProcessCrossrefLine
		case "pubmed":
			processLine = pubmed.ProcessPubmedLine
		default:
			panic("unknown upstream: " + in.Upstream)

		}

		err := workflow.ExecuteActivity(ctx, processLine, lin).Get(ctx, &c)
		if err != nil {
			return out, err
		}
		out = out.Add(c)
	}
	return out, nil
}

type scholkitScrapeInput struct {
	// Day value in format 2006-01-02
	Day string
	// Limit of items to fetch for a day; 0 or less for unlimited
	Limit int
	// Upstream is which API we're scraping
	Upstream string
}

type scholkitScrapeOutput struct {
	// S3Key points to a large .ndjson file of metadata from an upstream source
	S3Key string
}

func ScholkitScrapeActivity(ctx context.Context, in scholkitScrapeInput) (scholkitScrapeOutput, error) {
	out := scholkitScrapeOutput{}
	l := activity.GetLogger(ctx)

	limit := in.Limit
	if limit <= 0 {
		limit = viper.GetInt(fmt.Sprintf("%s.default_limit", in.Upstream))
	}

	s3bucket := viper.GetString(fmt.Sprintf("%s.scholkit_s3_bucket", in.Upstream))

	var syncStart time.Time
	var syncEnd time.Time
	var err error
	if in.Day != "" {
		syncStart, err = time.Parse("2006-01-02", in.Day)
		if err != nil {
			return out, err
		}
		syncEnd = syncStart.AddDate(0, 0, 1)
	} else {
		syncEnd = time.Now()
		syncStart = syncEnd.AddDate(0, 0, -1)
	}

	s3Prefix := in.Upstream + "/" + syncEnd.Format("2006") + "/"

	skPath := viper.GetString("scholkit.path")
	// TODO verify binary path?

	scholkitArgs := []string{
		"-s", in.Upstream,
		"-d", viper.GetString("scholkit.data_dir"),
		"-s3-upload",
		"-s3-endpoint", viper.GetString("s3.endpoint"),
		"-s3-access-key", viper.GetString("s3.access_id"),
		"-s3-secret-key", viper.GetString("s3.secret_key"),
		"-s3-bucket", s3bucket,
		"-s3-prefix", s3Prefix,
		"-sync-start", syncStart.Format("2006-01-02"),
		"-sync-end", syncEnd.Format("2006-01-02"),
	}

	if limit > 0 {
		scholkitArgs = append(scholkitArgs, "--limit", fmt.Sprintf("%d", limit))
	}

	l.Info(fmt.Sprintf("sk cmd: %s %s", skPath, scholkitArgs))
	cmd := exec.Command(skPath, scholkitArgs...)
	bs, err := cmd.Output()
	if err != nil {
		if errors.Is(err, &exec.ExitError{}) {
			l.Error("************* scholkit stderr start ****************")
			l.Error(string(err.(*exec.ExitError).Stderr))
			l.Error("************* scholkit stderr end ******************")
		}
		return out, fmt.Errorf("sk failed: %w", err)
	}

	l.Info(fmt.Sprintf("scholkit uploaded %s data to s3 key %s", in.Upstream, string(bs)))

	out.S3Key = strings.TrimSpace(string(bs))
	return out, nil
}

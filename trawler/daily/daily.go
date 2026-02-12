package daily

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/counts"
	"github.com/spf13/viper"
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
	out := counts.Counts{}
	//l := workflow.GetLogger(ctx)
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
		source = fmt.Sprintf("crossref-%s-%s", day, rid)
	}
	// fetch crossref metadata from the upstream API and store in s3

	ao := workflow.ActivityOptions{
		// TODO viperize
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString("crossref.external_task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	// TODO study skCrossref and determine how specific to crossref it is

	// TODO
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

func scholkitScrapeActivity(ctx context.Context, in scholkitScrapeInput) (scholkitScrapeOutput, error) {
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

	s3Prefix := syncEnd.Format("2006") + "/"

	skPath := viper.GetString("scholkit.path")
	// TODO verify binary path?

	scholkitArgs := []string{
		"-s", in.Upstream,
		"-d", viper.GetString("scholkit.data_dir"),
		fmt.Sprintf("--%s-upload-s3", in.Upstream),
		fmt.Sprintf("--%s-s3-rclone-remote", in.Upstream), "seaweed314",
		fmt.Sprintf("--%s-s3-bucket", in.Upstream), s3bucket,
		fmt.Sprintf("--%s-s3-prefix", in.Upstream), s3Prefix,
		fmt.Sprintf("--%s-sync-start", in.Upstream), syncStart.Format("2006-01-02"),
		fmt.Sprintf("--%s-sync-end", in.Upstream), syncEnd.Format("2006-01-02"),
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

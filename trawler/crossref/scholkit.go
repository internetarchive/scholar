package crossref

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
)

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
	l.Info(fmt.Sprintf("starting crossref harvest %#v", in))

	limit := in.Limit
	if limit == 0 {
		limit = viper.GetInt("crossref.daily_record_limit")
	}

	s3Bucket := viper.GetString("crossref.scholkit_s3_bucket")

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
		"-d", viper.GetString("scholkit.data_dir"),
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
	l.Info(fmt.Sprintf("sk cmd: %s %s", skPath, skArgs))
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

	l.Info(fmt.Sprintf("scholkit uploaded crossref data to s3 key %s", string(bs)))

	out.S3Key = strings.TrimSpace(string(bs))
	return
}

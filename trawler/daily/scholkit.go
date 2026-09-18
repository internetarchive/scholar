package daily

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

// The first half of a day: shell out to scholkit, which talks to the upstream
// API and leaves one big .ndjson of metadata in S3. Everything after this
// reads that object.
//
// It runs on its own task queue because it is the only part of the pipeline
// that needs to reach an upstream publisher API; the rest talks to petabox,
// fatcat and ES.

type scholkitScrapeInput struct {
	// Day value in format 2006-01-02
	Day string
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

	s3Prefix := in.Upstream + "/" + syncEnd.Format("2006")

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

	if in.Upstream == "crossref" {
		scholkitArgs = append(scholkitArgs, "-crossref-api-email", "scholar@archive.org")
		scholkitArgs = append(scholkitArgs, "-crossref-user-agent", "ia-scholar-trawler")
	}

	l.Info(fmt.Sprintf("sk cmd: %s %s", skPath, scholkitArgs))
	cmd := exec.Command(skPath, scholkitArgs...)
	bs, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil {
		if errors.As(err, &ee) {
			l.Error("************* scholkit stderr start ****************")
			l.Error(string(ee.Stderr))
			l.Error("************* scholkit stderr end ******************")
		}
		return out, fmt.Errorf("sk failed: %w", err)
	}

	s3key := strings.TrimSpace(string(bs))

	if s3key == "" {
		return out, errors.New("empty s3 key from scholkit")
	}

	l.Info(fmt.Sprintf("scholkit %s data to s3 key %q", in.Upstream, s3key))

	out.S3Key = s3key
	return out, nil
}

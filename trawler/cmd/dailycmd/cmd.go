package dailycmd

import (
	"fmt"
	"log"
	"slices"
	"time"

	"git.archive.org/webgroup/scholar/trawler/daily"
	"github.com/spf13/cobra"
)

var skDay string
var sourceOverride string
var upstream string
var concurrency int
var workflowID string

var upstreams = []string{"crossref", "arxiv", "doaj", "datacite", "pubmed"}

var Cmd = &cobra.Command{
	Use:   "daily",
	Short: "Work with daily crawls",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if !slices.Contains(upstreams, upstream) {
			return fmt.Errorf("expected one of %v; got '%s'", upstreams, upstream)
		}
		return nil

	},
}

func init() {
	Cmd.PersistentFlags().StringVar(&upstream, "upstream", "",
		"Which upstream source to crawl (crossref, arxiv, doaj, datacite, pubmed)")
	Cmd.MarkPersistentFlagRequired("upstream")

	StartOneOffCmd.Flags().StringVar(&skDay, "day", "",
		"Download metadata starting from midnight on specified day (format: 2006-01-02).")
	StartOneOffCmd.MarkFlagRequired("day")

	StartOneOffCmd.Flags().StringVar(&sourceOverride, "source", "",
		"Set a source string for any created Fatcat records. By default, a label is based on upstream/day.")

	StartOneOffCmd.Flags().IntVar(&concurrency, "concurrency", 0,
		"How many releases to crawl at once. Defaults to daily.concurrency from config.")

	SetConcurrencyCmd.Flags().IntVar(&concurrency, "concurrency", 0,
		"How many releases the running crawl should work at once. Takes effect on "+
			"the next batch it dispatches, not the ones already running.")
	SetConcurrencyCmd.MarkFlagRequired("concurrency")
	SetConcurrencyCmd.Flags().StringVar(&skDay, "day", "",
		"The day of the one-off run to retune (format: 2006-01-02).")
	SetConcurrencyCmd.Flags().StringVar(&workflowID, "workflow-id", "",
		"The Temporal workflow id to retune. Needed for scheduled runs, whose id "+
			"carries a timestamp; use --day instead for a one-off.")

	Cmd.AddCommand(StartScheduleCmd)
	Cmd.AddCommand(StartOneOffCmd)
	Cmd.AddCommand(SetConcurrencyCmd)
	Cmd.AddCommand(StartInternalWorkerCmd)
	Cmd.AddCommand(StartExternalWorkerCmd)
}

var StartScheduleCmd = &cobra.Command{
	Use:   "start-scheduler",
	Short: "start the schedule for daily crawling of an upstream",
	RunE: func(cmd *cobra.Command, args []string) error {
		crawlInput := daily.DailyCrawlWorkflowInput{
			SourceOverride: sourceOverride,
			Upstream:       upstream,
		}
		return daily.StartSchedule(crawlInput)
	},
}

var StartOneOffCmd = &cobra.Command{
	Use:   "one-off",
	Short: "Kick off a single crawl for one day's worth of metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		crawlInput := daily.DailyCrawlWorkflowInput{
			Day:            skDay,
			SourceOverride: sourceOverride,
			Upstream:       upstream,
			Concurrency:    concurrency,
		}
		return daily.StartOneOff(crawlInput)
	},
}

var SetConcurrencyCmd = &cobra.Command{
	Use:   "set-concurrency",
	Short: "Retune how many releases a running daily crawl works at once",
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := targetWorkflowID()
		if err != nil {
			return err
		}
		return daily.SetConcurrency(id, concurrency)
	},
}

// targetWorkflowID resolves which run to signal. A one-off's id is derived
// from upstream and day; a scheduled run's is not, because Temporal appends
// the scheduled time to it, so that one has to be named outright.
func targetWorkflowID() (string, error) {
	if workflowID != "" {
		return workflowID, nil
	}
	if skDay == "" {
		return "", fmt.Errorf("pass --day for a one-off run, or --workflow-id for a scheduled one")
	}
	day, err := parseDay(skDay)
	if err != nil {
		return "", err
	}
	return daily.WorkflowID(upstream, day), nil
}

var StartInternalWorkerCmd = &cobra.Command{
	Use:   "start-internal-worker",
	Short: "Start a Temporal worker that is intended to run in-cluster only",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Printf("starting %s internal worker", upstream)
		return daily.StartWorker(daily.WorkerDetails{
			Access:   "internal",
			Upstream: upstream,
		})
	},
}

var StartExternalWorkerCmd = &cobra.Command{
	Use:   "start-external-worker",
	Short: "Start a Temporal worker that requires internet access",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Printf("starting %s external worker", upstream)
		return daily.StartWorker(daily.WorkerDetails{
			Access:   "external",
			Upstream: upstream,
		})
	},
}

// parseDay turns the CLI's 2006-01-02 into the 20060102 the workflow id uses.
func parseDay(s string) (string, error) {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return "", fmt.Errorf("invalid day %q (want format 2006-01-02): %w", s, err)
	}
	return d.Format("20060102"), nil
}

package dailycmd

import (
	"fmt"
	"log"
	"slices"

	"git.archive.org/webgroup/scholar/trawler/daily"
	"github.com/spf13/cobra"
)

var skDay string
var sourceOverride string
var upstream string

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

	Cmd.AddCommand(StartScheduleCmd)
	Cmd.AddCommand(StartOneOffCmd)
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
		}
		return daily.StartOneOff(crawlInput)
	},
}

var StartInternalWorkerCmd = &cobra.Command{
	Use:   "start-internal-worker",
	Short: "Start a Temporal worker that is intended to run in-cluster only",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Printf("starting %s internal worker")
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

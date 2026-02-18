package crossrefcmd

import (
	"log"

	"git.archive.org/webgroup/scholar/trawler/daily"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "crossref",
	Short: "Work with the Crossref upstream",
}

var skLimit int
var skDay string
var sourceOverride string

func init() {
	StartOneOffCmd.Flags().IntVar(&skLimit, "limit", 0, "cap number of metadata entries to pull from crossref. Default: unlimited")
	StartOneOffCmd.Flags().StringVar(&skDay, "day", "", "Download metadata starting from midnight on specified day (format: 2006-01-02). Default: yesterday")
	StartOneOffCmd.Flags().StringVar(&sourceOverride, "source", "", "Set a source string for any created Fatcat records. By default, a label is generated.")

	Cmd.AddCommand(StartScheduleCmd)
	Cmd.AddCommand(StartOneOffCmd)
	Cmd.AddCommand(StartInternalWorkerCmd)
	Cmd.AddCommand(StartExternalWorkerCmd)
}

var StartScheduleCmd = &cobra.Command{
	Use:   "start-scheduler",
	Short: "start the schedule for daily crossref crawling",
	RunE: func(cmd *cobra.Command, args []string) error {
		crawlInput := daily.DailyCrawlWorkflowInput{
			Limit:          skLimit,
			SourceOverride: sourceOverride,
			Upstream:       "crossref",
		}
		return daily.StartSchedule(crawlInput)
	},
}

var StartOneOffCmd = &cobra.Command{
	Use:   "one-off",
	Short: "Kick off a single crawl for one day's worth of crossref metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		crawlInput := daily.DailyCrawlWorkflowInput{
			Day:            skDay,
			Limit:          skLimit,
			SourceOverride: sourceOverride,
			Upstream:       "crossref",
		}
		return daily.StartOneOff(crawlInput)
	},
}

var StartInternalWorkerCmd = &cobra.Command{
	Use:   "start-internal-worker",
	Short: "Start a Temporal worker that is intended to run in-cluster only",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Println("starting crossref internal worker")
		return daily.StartWorker(daily.WorkerDetails{
			Access:   "internal",
			Upstream: "crossref",
		})
	},
}

var StartExternalWorkerCmd = &cobra.Command{
	Use:   "start-external-worker",
	Short: "Start a Temporal worker that requires Internet access",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Println("starting crossref external worker")
		return daily.StartWorker(daily.WorkerDetails{
			Access:   "external",
			Upstream: "crossref",
		})
	},
}

package doajcmd

import (
	"log"

	"git.archive.org/webgroup/scholar/trawler/daily"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "doaj",
	Short: "Work with the DOAJ upstream",
}

var skDay string
var sourceOverride string

func init() {
	StartOneOffCmd.Flags().StringVar(&skDay, "day", "", "Download metadata starting from midnight on specified day (format: 2006-01-02). Default: yesterday")
	StartOneOffCmd.Flags().StringVar(&sourceOverride, "source", "", "Set a source string for any created Fatcat records. By default, a label is generated.")

	Cmd.AddCommand(StartScheduleCmd)
	Cmd.AddCommand(StartOneOffCmd)
	Cmd.AddCommand(StartInternalWorkerCmd)
	Cmd.AddCommand(StartExternalWorkerCmd)
}

var StartScheduleCmd = &cobra.Command{
	Use:   "start-scheduler",
	Short: "start the schedule for daily DOAJ crawling",
	RunE: func(cmd *cobra.Command, args []string) error {
		crawlInput := daily.DailyCrawlWorkflowInput{
			SourceOverride: sourceOverride,
			Upstream:       "doaj",
		}
		return daily.StartSchedule(crawlInput)
	},
}

var StartOneOffCmd = &cobra.Command{
	Use:   "one-off",
	Short: "Kick off a single crawl for one day's worth of DOAJ metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		crawlInput := daily.DailyCrawlWorkflowInput{
			Day:            skDay,
			SourceOverride: sourceOverride,
			Upstream:       "doaj",
		}
		return daily.StartOneOff(crawlInput)
	},
}

var StartInternalWorkerCmd = &cobra.Command{
	Use:   "start-internal-worker",
	Short: "Start a Temporal worker that is intended to run in-cluster only",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Println("starting doaj internal worker")
		return daily.StartWorker(daily.WorkerDetails{
			Access:   "internal",
			Upstream: "doaj",
		})
	},
}

var StartExternalWorkerCmd = &cobra.Command{
	Use:   "start-external-worker",
	Short: "Start a Temporal worker that requires Internet access",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Println("starting doaj external worker")
		return daily.StartWorker(daily.WorkerDetails{
			Access:   "external",
			Upstream: "doaj",
		})
	},
}

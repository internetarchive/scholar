package crossrefcmd

import (
	"git.archive.org/webgroup/scholar/trawler/crossref"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "crossref",
	Short: "Work with the Crossref upstream",
}

var skLimit int
var skDay string

func init() {
	StartOneOffCmd.Flags().IntVar(&skLimit, "limit", 0, "cap number of metadata entries to pull from crossref. Default: unlimited")
	StartOneOffCmd.Flags().StringVar(&skDay, "day", "", "Download metadata starting from midnight on specified day (format: 2006-01-02). Default: yesterday")

	Cmd.AddCommand(StartScheduleCmd)
	Cmd.AddCommand(StartOneOffCmd)
	Cmd.AddCommand(StartInternalWorkerCmd)
	Cmd.AddCommand(StartExternalWorkerCmd)
}

var StartScheduleCmd = &cobra.Command{
	Use:   "start-scheduler",
	Short: "start the schedule for daily crossref crawling",
	RunE: func(cmd *cobra.Command, args []string) error {
		return crossref.StartSchedule()
	},
}

var StartOneOffCmd = &cobra.Command{
	Use:   "one-off",
	Short: "Kick off a single crawl for one day's worth of crossref metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		crawlInput := crossref.CrossrefCrawlInput{
			SKInput: crossref.SKCrossrefInput{
				Day:   skDay,
				Limit: skLimit,
			},
		}
		return crossref.StartOneOff(crawlInput)
	},
}

var StartInternalWorkerCmd = &cobra.Command{
	Use:   "start-internal-worker",
	Short: "Start a Temporal worker that is intended to run in-cluster only",
	RunE: func(cmd *cobra.Command, args []string) error {
		return crossref.StartInternalWorker()
	},
}

var StartExternalWorkerCmd = &cobra.Command{
	Use:   "start-external-worker",
	Short: "Start a Temporal worker that requires Internet access",
	RunE: func(cmd *cobra.Command, args []string) error {
		return crossref.StartExternalWorker()
	},
}

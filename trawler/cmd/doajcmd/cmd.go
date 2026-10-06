package doajcmd

import (
	"log"

	"git.archive.org/webgroup/scholar/trawler/doaj"
	"github.com/spf13/cobra"
)

var sourceOverride string
var s3key string

var Cmd = &cobra.Command{
	Use:   "doaj-journals",
	Short: "Keep fatcat's containers up to date with DOAJ's journal list",
	Long: `Fetch DOAJ's journal CSV (https://doaj.org/csv) and create a fatcat
container for every journal in it that fatcat does not already have, so that
article importers can find it by ISSN.

Existing containers are never modified.`,
}

func init() {
	StartOneOffCmd.Flags().StringVar(&sourceOverride, "source", "",
		"Set a source string for created containers. By default, a label is based on the date and run.")
	StartOneOffCmd.Flags().StringVar(&s3key, "s3-key", "",
		"Import a journal CSV already in S3 instead of fetching a fresh one (format: bucket/key).")

	Cmd.AddCommand(StartScheduleCmd)
	Cmd.AddCommand(StartOneOffCmd)
	Cmd.AddCommand(StartInternalWorkerCmd)
	Cmd.AddCommand(StartExternalWorkerCmd)
}

var StartScheduleCmd = &cobra.Command{
	Use:   "start-scheduler",
	Short: "Start the daily schedule for importing DOAJ journals",
	RunE: func(cmd *cobra.Command, args []string) error {
		return doaj.StartJournalsSchedule(doaj.JournalsWorkflowInput{})
	},
}

var StartOneOffCmd = &cobra.Command{
	Use:   "one-off",
	Short: "Run a single DOAJ journal import now",
	RunE: func(cmd *cobra.Command, args []string) error {
		return doaj.StartJournalsOneOff(doaj.JournalsWorkflowInput{
			SourceOverride: sourceOverride,
			S3Key:          s3key,
		})
	},
}

var StartInternalWorkerCmd = &cobra.Command{
	Use:   "start-internal-worker",
	Short: "Start a Temporal worker that is intended to run in-cluster only",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Print("starting doaj-journals internal worker")
		return doaj.StartJournalsWorker("internal")
	},
}

var StartExternalWorkerCmd = &cobra.Command{
	Use:   "start-external-worker",
	Short: "Start a Temporal worker that requires internet access",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Print("starting doaj-journals external worker")
		return doaj.StartJournalsWorker("external")
	},
}

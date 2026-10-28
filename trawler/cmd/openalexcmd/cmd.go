package openalexcmd

import (
	"errors"
	"fmt"
	"log"
	"time"

	"git.archive.org/webgroup/scholar/trawler/openalex"
	"github.com/spf13/cobra"
)

var date string
var sourceOverride string
var s3key string

var Cmd = &cobra.Command{
	Use:   "openalex-sources",
	Short: "Add containers to fatcat from OpenAlex's sources",
	Long: `Fetch one day of updated source records from OpenAlex's snapshot
(s3://openalex/data/jsonl/sources/updated_date=YYYY-MM-DD/) and create a fatcat
container for every journal, conference or book series in it that fatcat does
not already have, so that article importers can find it by ISSN-L.

Existing containers are never modified.`,
}

func init() {
	StartOneOffCmd.Flags().StringVar(&date, "date", "",
		"The updated_date partition to import, as YYYY-MM-DD.")
	StartOneOffCmd.Flags().StringVar(&sourceOverride, "source", "",
		"Set a source string for created containers. By default, a label is based on the date and run.")
	StartOneOffCmd.Flags().StringVar(&s3key, "s3-key", "",
		"Import a day already in S3 instead of fetching it (format: bucket/key).")

	Cmd.AddCommand(StartOneOffCmd)
	Cmd.AddCommand(StartInternalWorkerCmd)
	Cmd.AddCommand(StartExternalWorkerCmd)
}

var StartOneOffCmd = &cobra.Command{
	Use:   "one-off",
	Short: "Import a single day of OpenAlex source updates now",
	RunE: func(cmd *cobra.Command, args []string) error {
		if date == "" && s3key == "" {
			return errors.New("one of --date or --s3-key is required")
		}
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return fmt.Errorf("--date must be YYYY-MM-DD: %w", err)
			}
		}
		return openalex.StartSourcesOneOff(openalex.SourcesWorkflowInput{
			Date:           date,
			SourceOverride: sourceOverride,
			S3Key:          s3key,
		})
	},
}

var StartInternalWorkerCmd = &cobra.Command{
	Use:   "start-internal-worker",
	Short: "Start a Temporal worker that is intended to run in-cluster only",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Print("starting openalex-sources internal worker")
		return openalex.StartSourcesWorker("internal")
	},
}

var StartExternalWorkerCmd = &cobra.Command{
	Use:   "start-external-worker",
	Short: "Start a Temporal worker that requires internet access",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Print("starting openalex-sources external worker")
		return openalex.StartSourcesWorker("external")
	},
}

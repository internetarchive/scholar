package pdfcmd

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/pdf"
	"git.archive.org/webgroup/scholar/trawler/periodic"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "pdf",
	Short: "Work with PDFs",
}

var dumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Dump PDF content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dumped, err := pdf.Dump(args[0])
		if err != nil {
			return err
		}

		out, err := json.Marshal(dumped)
		if err != nil {
			return fmt.Errorf("could not serialize dumped pdf: %w", err)
		}

		fmt.Println(string(out))

		return nil
	},
}

var ingestLimit int

var ingestCmd = &cobra.Command{
	Use:   "ingest COLLECTION_URL_OR_ID",
	Short: "Kick off a periodic-ingest workflow over an IA collection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := parseCollectionArg(args[0])
		if id == "" {
			return fmt.Errorf("could not extract collection id from %q", args[0])
		}
		return periodic.StartCollectionIngest(periodic.PeriodicIngestInput{
			CollectionName: id,
			Limit:          ingestLimit,
		})
	},
}

var startPeriodicIngestWorkerCmd = &cobra.Command{
	Use:   "start-periodic-ingest-worker",
	Short: "Start the Temporal worker for periodic-ingest workflows",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Println("starting periodic ingest worker")
		return periodic.StartWorker()
	},
}

// parseCollectionArg accepts either an archive.org "/details/<id>" URL or a
// bare collection identifier and returns the identifier.
func parseCollectionArg(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "/details/"); i >= 0 {
		s = s[i+len("/details/"):]
	}
	s = strings.TrimSuffix(s, "/")
	return s
}

func init() {
	ingestCmd.Flags().IntVar(&ingestLimit, "limit", 0,
		"max number of items to select from the collection (0 = no limit). Useful for smoke tests.")

	Cmd.AddCommand(dumpCmd)
	Cmd.AddCommand(ingestCmd)
	Cmd.AddCommand(startPeriodicIngestWorkerCmd)
}

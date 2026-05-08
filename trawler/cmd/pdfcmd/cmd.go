package pdfcmd

import (
	"encoding/json"
	"fmt"

	"git.archive.org/webgroup/scholar/trawler/pdf"
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

		fmt.Println(out)

		return nil
	},
}

var ingestCmd = &cobra.Command{
	Use:   "ingest",
	Short: "Ingest a PDF",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented")
	},
}

func init() {
	Cmd.AddCommand(dumpCmd)
	Cmd.AddCommand(ingestCmd)
}

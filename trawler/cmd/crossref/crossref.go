package crossref

import (
	"fmt"

	"git.archive.org/webgroup/scholar/trawler/crossref"
	"github.com/spf13/cobra"
)

var processTypes = []string{"starter", "worker"}
var processType string

func init() {
	startCmd.Flags().StringVar(&processType, "type", "", fmt.Sprintf("Type of process to start (%v)", processTypes))
	Cmd.MarkFlagRequired("type")
	Cmd.AddCommand(startCmd)
}

var Cmd = &cobra.Command{
	Use:   "crossref",
	Short: "work with the Crossref upstream",
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "start a long running process for trawling crossref",
	PreRunE: func(cmd *cobra.Command, args []string) error {
		var validType bool
		for _, t := range processTypes {
			if processType == t {
				validType = true
			}
		}
		if !validType {
			return fmt.Errorf("expected one of %v, got %s", processTypes, processType)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if processType == "starter" {
			return crossref.RunStarter()
		} else if processType == "worker" {
			return crossref.RunWorker()
		} else {
			panic("unreachable")
		}
	},
}

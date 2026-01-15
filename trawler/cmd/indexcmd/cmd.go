package indexcmd

import (
	"fmt"
	"slices"

	"git.archive.org/webgroup/scholar/trawler/indexing"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var IndexCmd = &cobra.Command{
	Use:   "index [uuid]",
	Short: "Insert or update a document in elasticsearch by fc2 id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		u, err := uuid.Parse(args[0])
		if err != nil {
			return err
		}

		if !slices.Contains(flavors, flavor) {
			return fmt.Errorf("valid flavors: %v", flavors)
		}

		var f func(uuid.UUID) error

		switch flavor {
		case "container":
			f = indexing.IndexContainer
		case "file":
			f = indexing.IndexFile
		case "release":
			f = indexing.IndexRelease
		case "fulltext":
			// TODO
		default:
			panic("unreachable")
		}

		return f(u)
	},
}

var flavor string
var flavors = []string{"container", "file", "fulltext", "release"}

func init() {
	IndexCmd.Flags().StringVarP(&flavor, "flavor", "f", "", fmt.Sprintf("flavor of idexing: %v", flavors))
	IndexCmd.MarkFlagRequired("flavor")
}

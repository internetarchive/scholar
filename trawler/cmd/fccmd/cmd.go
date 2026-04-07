package fccmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "fc",
	Short: "Interact with the fatcat2 API",
}

var entityType string

var getTypes = []string{"release", "container", "file", "creator"}
var createTypes = []string{"release", "container", "file"}

var getCmd = &cobra.Command{
	Use:   "get [uuid]",
	Short: "Get an entity by UUID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := uuid.Parse(args[0])
		if err != nil {
			return err
		}

		c := http.DefaultClient
		var result any

		switch entityType {
		case "release":
			result, err = fatcat2.GetRelease(c, id)
		case "container":
			result, err = fatcat2.GetContainer(c, id)
		case "file":
			result, err = fatcat2.GetFile(c, id)
		case "creator":
			result, err = fatcat2.GetCreator(c, id)
		default:
			return fmt.Errorf("valid types: %v", getTypes)
		}

		if err != nil {
			return err
		}

		bs, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}

		fmt.Println(string(bs))
		return nil
	},
}

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an entity from JSON on STDIN",
	RunE: func(cmd *cobra.Command, args []string) error {
		bs, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("could not read stdin: %w", err)
		}

		c := http.DefaultClient
		var id *uuid.UUID

		switch entityType {
		case "release":
			var r fatcat2.Release
			if err := json.Unmarshal(bs, &r); err != nil {
				return fmt.Errorf("could not parse release JSON: %w", err)
			}
			id, err = fatcat2.CreateRelease(c, r)
		case "container":
			var cont fatcat2.Container
			if err := json.Unmarshal(bs, &cont); err != nil {
				return fmt.Errorf("could not parse container JSON: %w", err)
			}
			id, err = fatcat2.CreateContainer(c, &cont)
		case "file":
			var f fatcat2.File
			if err := json.Unmarshal(bs, &f); err != nil {
				return fmt.Errorf("could not parse file JSON: %w", err)
			}
			id, err = fatcat2.CreateFile(c, &f)
		default:
			return fmt.Errorf("valid types: %v", createTypes)
		}

		if err != nil {
			return err
		}

		fmt.Println(id.String())
		return nil
	},
}

var lookupTypes = []string{"doi", "pmid", "arxiv", "orcid", "issnl", "doaj", "sha256"}

var lookupCmd = &cobra.Command{
	Use:   "lookup [id-type] [value]",
	Short: "Look up an entity UUID by external ID",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		idType := args[0]
		idValue := args[1]

		c := http.DefaultClient
		var id *uuid.UUID
		var err error

		switch idType {
		case "doi":
			id, err = fatcat2.LookupDoi(c, idValue)
		case "pmid":
			id, err = fatcat2.LookupPmid(c, idValue)
		case "arxiv":
			id, err = fatcat2.LookupArxiv(c, idValue)
		case "orcid":
			id, err = fatcat2.LookupOrcid(c, idValue)
		case "issnl":
			id, err = fatcat2.LookupIssnl(c, idValue)
		case "doaj":
			id, err = fatcat2.LookupDoaj(c, idValue)
		case "sha256":
			id, err = fatcat2.LookupSha256(c, idValue)
		default:
			return fmt.Errorf("valid id types: %v", lookupTypes)
		}

		if err != nil {
			return err
		}

		if id == nil {
			return fmt.Errorf("not found: %s %s", idType, idValue)
		}

		fmt.Println(id.String())
		return nil
	},
}

func init() {
	getCmd.Flags().StringVarP(&entityType, "type", "t", "", fmt.Sprintf("entity type: %v", getTypes))
	getCmd.MarkFlagRequired("type")

	createCmd.Flags().StringVarP(&entityType, "type", "t", "", fmt.Sprintf("entity type: %v", createTypes))
	createCmd.MarkFlagRequired("type")

	Cmd.AddCommand(getCmd)
	Cmd.AddCommand(createCmd)
	Cmd.AddCommand(lookupCmd)
}

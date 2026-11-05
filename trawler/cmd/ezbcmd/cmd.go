package ezbcmd

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"

	"git.archive.org/webgroup/scholar/trawler/ezb"
	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"github.com/spf13/cobra"
)

const defaultJournalsURL = "https://ezb-api.ur.de/journals"

var localFile string
var outDir string
var workers int
var apply bool

var Cmd = &cobra.Command{
	Use:   "ezb-backfill",
	Short: "Update fatcat's existing containers from a dump of EZB's journal list",
	Long: `Bring fatcat's existing containers up to date from the Electronic Journals
Library's journal list (https://ezb-api.ur.de/journals). A one-off: run by hand,
not scheduled.

For each journal in the list whose container strongly matches by ISSN, it
rewrites extra.ezb (EZB and ZDB IDs, price type, coverage years, and EZB's open
access and diamond labels), and fills in publisher, ISSNs and homepage where the
container has none. It never overwrites other values and never creates a
container. Each container it changes is re-indexed.

Dry-run by default: makes only fatcat's container lookups and reads, and writes
what it would change. Pass --apply to write to fatcat. Re-running is safe;
containers already up to date are not written again.

Writes to --out-dir:
  updated.ndjson     containers as they are (or would be) after updating
  conflicted.ndjson  journals matched to a container carrying another
                     journal's ISSN, left alone for a look by hand`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		var r io.Reader
		if localFile != "" {
			f, err := os.Open(localFile)
			if err != nil {
				return err
			}
			defer f.Close()
			r = f
		} else {
			log.Printf("fetching %s (EZB builds it on request; this takes a few minutes)", defaultJournalsURL)
			bs, err := httpclient.GetBody(ctx, httpclient.Shared(), defaultJournalsURL)
			if err != nil {
				return err
			}
			r = bytes.NewReader(bs)
		}

		updated, err := os.Create(filepath.Join(outDir, "updated.ndjson"))
		if err != nil {
			return err
		}
		defer updated.Close()
		conflicted, err := os.Create(filepath.Join(outDir, "conflicted.ndjson"))
		if err != nil {
			return err
		}
		defer conflicted.Close()

		if apply {
			log.Print("APPLY: writing to fatcat")
		} else {
			log.Print("DRY-RUN: nothing is written to fatcat; pass --apply to write")
		}

		counts, err := ezb.Backfill(ctx, httpclient.Shared(), r, apply, workers,
			ezb.BackfillOutput{Updated: updated, Conflicted: conflicted},
			func(c ezb.Counts) { log.Printf("progress: %+v", c) })
		log.Printf("done: %+v", counts)
		return err
	},
}

func init() {
	Cmd.Flags().StringVar(&localFile, "file", "",
		"Read the journal list from this file instead of fetching it from EZB. Either EZB's JSON\n"+
			"(https://ezb-api.ur.de/journals) or its TSV\n"+
			"(https://ezb.ur.de/services/titlelist?collection_id=journals); the TSV lacks EZB's\n"+
			"Diamond label.")
	Cmd.Flags().StringVar(&outDir, "out-dir", ".",
		"Write updated.ndjson and conflicted.ndjson here.")
	Cmd.Flags().IntVar(&workers, "workers", 4,
		"How many journals to look up in fatcat at once.")
	Cmd.Flags().BoolVar(&apply, "apply", false,
		"Write to fatcat. Without it, this is a dry run.")
}

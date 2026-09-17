package pdfcmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxfile"
	"git.archive.org/webgroup/scholar/trawler/ia"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"git.archive.org/webgroup/scholar/trawler/periodic"
	"github.com/spf13/cobra"
)

// cdxItemsPerPage is the advancedsearch page size used to enumerate a
// collection's items. Matches periodic.itemsPerPage.
const cdxItemsPerPage = 100

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

var (
	ingestConcurrency   int
	ingestTaskQueue     string
	ingestSource        string
	ingestStartAtItem   string
	ingestStartAtOffset int64
)

var ingestWarcsCmd = &cobra.Command{
	Use:   "ingest-warcs COLLECTION_URL_OR_ID",
	Short: "Kick off a periodic-ingest workflow over an IA collection, ingesting pdfs from warcs",
	Long: `Walk an IA crawl collection, pulling PDFs out of its WARCs and turning
them into fatcat file records and search documents.

Every row is idempotent -- a PDF already in fatcat and both indices is
recognised and passed over -- but recognising it still costs lookups. To pick up
where a previous run of this collection stopped rather than re-checking what it
already did, pass --start-at-item (and optionally --start-at-offset).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		collName := parseCollectionArg(args[0])
		if collName == "" {
			return fmt.Errorf("could not extract collection name from %q", args[0])
		}
		return periodic.StartCollectionIngest(periodic.PeriodicIngestInput{
			CollectionName: collName,
			Concurrency:    ingestConcurrency,
			TaskQueue:      ingestTaskQueue,
			SourceOverride: ingestSource,
			StartAtItem:    ingestStartAtItem,
			StartAtOffset:  ingestStartAtOffset,
		})
	},
}

var setIngestConcurrencyCmd = &cobra.Command{
	Use:   "set-ingest-concurrency COLLECTION_URL_OR_ID",
	Short: "Retune how many PDFs a running periodic ingest works at once",
	Long: `Signal a running periodic-ingest workflow to change its concurrency.

The new value reaches the batches dispatched after the signal rather than the
ones already running, so it takes a batch to bite. This is the knob to reach for
when GROBID or petabox is unhappy and the alternative would be terminating a run
that is hours deep into a collection.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		collName := parseCollectionArg(args[0])
		if collName == "" {
			return fmt.Errorf("could not extract collection name from %q", args[0])
		}
		return periodic.SetConcurrency(collName, ingestConcurrency)
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

var cdxCmd = &cobra.Command{
	Use:   "cdx COLLECTION_URL_OR_ID",
	Short: "Dump the PDF CDX rows (cdxfile.PDFFilter) for an IA collection",
	Long: `Enumerate an IA collection, and for each item find its rollup CDX, apply
cdxfile.PDFFilter, and print the selected rows to stdout (one per line).

This is the same CDX selection the periodic-ingest pipeline feeds to PDF
processing, exposed for inspection. Items that lack a usable rollup CDX are
skipped with a note on stderr so a single bad item doesn't abort the dump.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := parseCollectionArg(args[0])
		if id == "" {
			return fmt.Errorf("could not extract collection id from %q", args[0])
		}
		return dumpCollectionPDFCDX(cmd.Context(), id)
	},
}

// dumpCollectionPDFCDX pages through an IA collection and writes each item's
// PDFFilter-selected rollup CDX rows to stdout. Per-item failures are logged
// to stderr and skipped rather than aborting the whole dump.
func dumpCollectionPDFCDX(ctx context.Context, collectionID string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	page := 1
	for {
		var ids []string
		var hasMore bool
		err := retryNetwork(ctx, fmt.Sprintf("search collection %q page %d", collectionID, page),
			func() error {
				var e error
				ids, hasMore, e = ia.SearchCollection(ctx, client, collectionID, page, cdxItemsPerPage)
				return e
			})
		if err != nil {
			return fmt.Errorf("search collection %q: %w", collectionID, err)
		}
		for _, id := range ids {
			if strings.HasSuffix(id, "-CRL") {
				continue
			}
			err := retryNetwork(ctx, "item "+id, func() error {
				return dumpItemPDFCDX(ctx, client, id, out)
			})
			if err != nil {
				log.Printf("skipping item %s: %v", id, err)
			}
		}
		if !hasMore {
			break
		}
		page++
	}
	return nil
}

// cdxRetryMaxAttempts and cdxRetryMaxBackoff bound the exponential backoff
// retryNetwork applies to transient network failures.
const (
	cdxRetryMaxAttempts = 8
	cdxRetryMaxBackoff  = 60 * time.Second
)

// isRetryableNetErr reports whether err is a transient network-layer failure
// worth retrying (DNS lookup failure, connection refused/reset, timeout).
// Definitive results — a bad HTTP status, a decode error, or ia.ErrNoCDX /
// ia.ErrMultipleCDX — are not network errors and so are not retried.
func isRetryableNetErr(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}

// retryNetwork runs fn, retrying with capped exponential backoff while it
// returns a transient network error and ctx is alive. Success and
// non-retryable errors return immediately; retryable errors that outlast
// cdxRetryMaxAttempts are returned so the caller can skip.
func retryNetwork(ctx context.Context, label string, fn func() error) error {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !isRetryableNetErr(err) {
			return err
		}
		if attempt >= cdxRetryMaxAttempts {
			return fmt.Errorf("%s: giving up after %d attempts: %w", label, attempt, err)
		}
		log.Printf("%s: transient network error (attempt %d/%d), retrying in %s: %v",
			label, attempt, cdxRetryMaxAttempts, backoff, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > cdxRetryMaxBackoff {
			backoff = cdxRetryMaxBackoff
		}
	}
}

// dumpItemPDFCDX finds an item's rollup CDX, applies cdxfile.PDFFilter, and
// writes each matching row to w.
func dumpItemPDFCDX(ctx context.Context, c *http.Client, itemID string, w io.Writer) error {
	files, err := ia.ItemFiles(ctx, c, itemID)
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	rollup, err := ia.FindRollupCDX(files)
	if err != nil {
		return fmt.Errorf("find rollup CDX: %w", err)
	}
	rdr, err := ia.OpenFile(ctx, c, itemID, rollup)
	if err != nil {
		return fmt.Errorf("open rollup CDX %s: %w", rollup, err)
	}
	defer rdr.Close()

	rows, err := cdxfile.Parse(rdr, cdxfile.PDFFilter)
	if err != nil {
		return fmt.Errorf("parse rollup CDX %s: %w", rollup, err)
	}
	for _, row := range rows {
		fmt.Fprintln(w, row.String())
	}
	return nil
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
	ingestWarcsCmd.Flags().IntVar(&ingestConcurrency, "concurrency", 0,
		"How many PDFs to ingest at once. Defaults to periodic.concurrency from config.")
	ingestWarcsCmd.Flags().StringVar(&ingestTaskQueue, "task-queue", "",
		"Override the task queue. Defaults to periodic.task_queue from config.")
	ingestWarcsCmd.Flags().StringVar(&ingestSource, "source", "",
		"Override the provenance label stamped on records this run creates.")
	ingestWarcsCmd.Flags().StringVar(&ingestStartAtItem, "start-at-item", "",
		"Begin at this item rather than the top of the collection, skipping "+
			"everything a previous run already got through. Fails if the item is "+
			"not in the collection.")
	ingestWarcsCmd.Flags().Int64Var(&ingestStartAtOffset, "start-at-offset", 0,
		"Byte offset into --start-at-item's staged PDF CDX to begin at. Requires "+
			"--start-at-item; omit it to redo that one item from the top.")

	setIngestConcurrencyCmd.Flags().IntVar(&ingestConcurrency, "concurrency", 0,
		"How many PDFs the running ingest should work at once. Takes effect on "+
			"the next batch it dispatches, not the ones already running.")
	setIngestConcurrencyCmd.MarkFlagRequired("concurrency")

	Cmd.AddCommand(dumpCmd)
	Cmd.AddCommand(cdxCmd)
	Cmd.AddCommand(ingestWarcsCmd)
	Cmd.AddCommand(setIngestConcurrencyCmd)
	Cmd.AddCommand(startPeriodicIngestWorkerCmd)
}

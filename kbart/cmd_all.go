package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/internetarchive/scholar/kbart/internal/es"
	"github.com/internetarchive/scholar/kbart/internal/kbart"
	"github.com/spf13/cobra"
)

var (
	allSimFile  string
	allISSNMap  string
	allOutdir   string
	allDate     string
	allThisYear int
	allQuery    string
	allBatch    int
)

var allCmd = &cobra.Command{
	Use:   "all",
	Short: "Run the full pipeline: search, report, sim, combine",
	Long: "all runs the whole report end-to-end: it searches fatcat_container for\n" +
		"candidates, checks their eligibility against the fatcat v2 API, converts the\n" +
		"IA SIM serials file, and combines both into the report submitted to the\n" +
		"Keepers Registry. It writes three dated files into --outdir:\n" +
		"  fatcat_kbart.<date>.tsv\n" +
		"  ia_sim_keepers_kbart.<date>.tsv\n" +
		"  ia_serials_combined_kbart.<date>.tsv",
	RunE: runAll,
}

func init() {
	f := allCmd.Flags()
	f.StringVar(&allSimFile, "sim-file", "", "IA SIM serials KBART file (required)")
	f.StringVar(&allISSNMap, "issn-map", "", "ISSN-to-ISSN-L table file (required)")
	f.StringVar(&allOutdir, "outdir", ".", "directory to write the dated output files into")
	f.StringVar(&allDate, "date", time.Now().Format("2006-01-02"), "date stamp for output filenames")
	f.IntVar(&allThisYear, "this-year", time.Now().Year(), "current year (last year/volume of an ongoing span is dropped)")
	f.StringVar(&allQuery, "query", es.ContainerQuery, "Elasticsearch query_string for the container search")
	f.IntVar(&allBatch, "batch", 1000, "scroll page size for the search")
	allCmd.MarkFlagRequired("sim-file")
	allCmd.MarkFlagRequired("issn-map")
	rootCmd.AddCommand(allCmd)
}

func runAll(cmd *cobra.Command, args []string) error {
	fatcatPath := filepath.Join(allOutdir, "fatcat_kbart."+allDate+".tsv")
	simPath := filepath.Join(allOutdir, "ia_sim_keepers_kbart."+allDate+".tsv")
	combinedPath := filepath.Join(allOutdir, "ia_serials_combined_kbart."+allDate+".tsv")

	// 1. Search fatcat_container for candidate idents.
	fmt.Fprintln(os.Stderr, "[1/4] searching fatcat_container...")
	idents, err := collectIdents(allQuery, allBatch)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	fmt.Fprintf(os.Stderr, "      %d candidate containers\n", len(idents))

	// 2. Fetch + evaluate eligibility, writing the fatcat KBART file.
	fmt.Fprintf(os.Stderr, "[2/4] checking eligibility -> %s\n", fatcatPath)
	reportThisYear = allThisYear
	reportDumpJSON = false
	if err := writeToFile(fatcatPath, func(w *bufio.Writer) error {
		return fetchAndEmit(idents, w)
	}); err != nil {
		return fmt.Errorf("report: %w", err)
	}

	// 3. Convert the IA SIM serials file.
	fmt.Fprintf(os.Stderr, "[3/4] converting SIM serials -> %s\n", simPath)
	m, err := kbart.LoadISSNMap(allISSNMap)
	if err != nil {
		return fmt.Errorf("load issn map: %w", err)
	}
	simIn, err := os.Open(allSimFile)
	if err != nil {
		return err
	}
	defer simIn.Close()
	if err := writeToFile(simPath, func(w *bufio.Writer) error {
		written, skipped, err := kbart.ConvertSIM(simIn, m, w)
		fmt.Fprintf(os.Stderr, "      %d rows written, %d skipped\n", written, skipped)
		return err
	}); err != nil {
		return fmt.Errorf("sim: %w", err)
	}

	// 4. Combine into the file submitted to the Keepers Registry.
	fmt.Fprintf(os.Stderr, "[4/4] combining -> %s\n", combinedPath)
	if err := writeToFile(combinedPath, func(w *bufio.Writer) error {
		return combineFiles([]string{fatcatPath, simPath}, w)
	}); err != nil {
		return fmt.Errorf("combine: %w", err)
	}

	fmt.Fprintln(os.Stderr, "done.")
	return nil
}

// collectIdents scrolls the container search and returns all matching idents.
func collectIdents(query string, batch int) ([]string, error) {
	client := es.NewClient(esHost)
	var idents []string
	err := client.ScrollContainers(query, batch, func(h es.Hit) error {
		idents = append(idents, h.Ident)
		return nil
	})
	return idents, err
}

// writeToFile creates path and runs fn against a buffered writer over it.
func writeToFile(path string, fn func(*bufio.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	if err := fn(w); err != nil {
		return err
	}
	return w.Flush()
}

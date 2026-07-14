package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/internetarchive/scholar/kbart/internal/fatcat"
	"github.com/internetarchive/scholar/kbart/internal/fcid"
	"github.com/internetarchive/scholar/kbart/internal/kbart"
	"github.com/spf13/cobra"
)

var (
	reportIn       string
	reportOut      string
	reportDumpJSON bool
	reportFromJSON bool
	reportThisYear int
	reportVerbose  bool
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Turn candidate container idents into eligible KBART rows",
	Long: "report reads container idents (one per line, or JSON objects with an\n" +
		"\"ident\" field), checks each against the fatcat v2 API for KBART eligibility,\n" +
		"and writes KBART TSV rows for the eligible ones. It replaces fatcat_kbart.py.\n\n" +
		"--dump-json writes the full fetched metadata + status as JSONL (for debugging\n" +
		"or caching); --from-json reads that JSONL back and emits KBART without\n" +
		"re-fetching.",
	RunE: runReport,
}

func init() {
	f := reportCmd.Flags()
	f.StringVarP(&reportIn, "in", "i", "", "read input from this file instead of stdin")
	f.StringVarP(&reportOut, "out", "o", "", "write output to this file instead of stdout")
	f.BoolVar(&reportDumpJSON, "dump-json", false, "emit full fetched metadata + status as JSONL")
	f.BoolVar(&reportFromJSON, "from-json", false, "read --dump-json JSONL instead of fetching")
	f.IntVar(&reportThisYear, "this-year", time.Now().Year(), "current year (last year/volume of an ongoing span is dropped)")
	f.BoolVarP(&reportVerbose, "verbose", "v", false, "log every container's status to stderr")
	rootCmd.AddCommand(reportCmd)
}

func runReport(cmd *cobra.Command, args []string) error {
	if reportDumpJSON && reportFromJSON {
		return fmt.Errorf("--dump-json and --from-json are mutually exclusive")
	}

	in := os.Stdin
	if reportIn != "" {
		f, err := os.Open(reportIn)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	out := os.Stdout
	if reportOut != "" {
		f, err := os.Create(reportOut)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}

	if reportFromJSON {
		return reportFromJSONMode(in, out)
	}
	return reportFetchMode(in, out)
}

// reportFetchMode reads idents and fetches from the fatcat v2 API.
func reportFetchMode(in io.Reader, out io.Writer) error {
	idents, err := readIdents(in)
	if err != nil {
		return err
	}
	return fetchAndEmit(idents, out)
}

// fetchAndEmit classifies each ident concurrently (preserving input order) and
// writes the result (TSV or, under --dump-json, JSONL). Reused by `all`.
func fetchAndEmit(idents []string, out io.Writer) error {
	fc := fatcat.NewClient(apiHost)

	results := make([]*kbart.Info, len(idents))
	var wg sync.WaitGroup
	jobs := make(chan int)
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = evaluateIdent(fc, idents[i])
			}
		}()
	}
	for i := range idents {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	return emitResults(out, results)
}

// evaluateIdent fetches and classifies a single container. A fetch error is
// recorded as the status "fetch-error" (the container is skipped downstream),
// mirroring the Python RetryError path.
func evaluateIdent(fc *fatcat.Client, ident string) *kbart.Info {
	info := &kbart.Info{Ident: ident}
	uuid, err := fcid.ToUUID(ident)
	if err != nil {
		info.Status = "bad-ident"
		return info
	}
	info.UUID = uuid

	src := &kbart.LiveSource{FC: fc, UUID: uuid, Ident: ident}
	status, err := kbart.Evaluate(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "container_%s: %v\n", ident, err)
		info.Status = "fetch-error"
		return info
	}
	info.Status = status
	info.Eligible = status == kbart.StatusSuccess

	if reportDumpJSON {
		// Populate the full Info for the JSON dump. These are all cached by the
		// LiveSource, except any endpoints an early rejection never reached.
		info.Container, _ = src.Container()
		info.Stats, _ = src.Stats()
		info.ByYear, _ = src.ByYear()
		info.ByVolume, _ = src.ByVolume()
		info.ByType, _ = src.ByType()
	} else if info.Eligible {
		row, err := kbart.ToRow(src, reportThisYear)
		if err != nil {
			fmt.Fprintf(os.Stderr, "container_%s: %v\n", ident, err)
			info.Status = "row-error"
			info.Eligible = false
			return info
		}
		info.Row = &row
	}
	return info
}

// reportFromJSONMode reads dumped Info JSONL and emits KBART without fetching.
func reportFromJSONMode(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var results []*kbart.Info
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var info kbart.Info
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			return err
		}
		src := kbart.NewStaticSource(&info)
		if info.Status == "" {
			status, err := kbart.Evaluate(src)
			if err != nil {
				return err
			}
			info.Status = status
		}
		info.Eligible = info.Status == kbart.StatusSuccess
		if info.Eligible {
			row, err := kbart.ToRow(src, reportThisYear)
			if err != nil {
				fmt.Fprintf(os.Stderr, "container_%s: %v\n", info.Ident, err)
				info.Eligible = false
			} else {
				info.Row = &row
			}
		}
		results = append(results, &info)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return emitResults(out, results)
}

// emitResults writes either the JSONL dump or the KBART TSV, plus a status
// tally to stderr, preserving input order.
func emitResults(out io.Writer, results []*kbart.Info) error {
	w := bufio.NewWriter(out)
	defer w.Flush()

	tally := map[string]int{}
	if reportDumpJSON {
		enc := json.NewEncoder(w)
		for _, info := range results {
			if info == nil {
				continue
			}
			tally[info.Status]++
			if err := enc.Encode(info); err != nil {
				return err
			}
		}
	} else {
		kw := kbart.NewWriter(w)
		if err := kw.WriteHeader(); err != nil {
			return err
		}
		for _, info := range results {
			if info == nil {
				continue
			}
			tally[info.Status]++
			if reportVerbose {
				fmt.Fprintf(os.Stderr, "container_%s\t%s\n", info.Ident, info.Status)
			}
			if info.Eligible && info.Row != nil {
				if err := kw.Write(*info.Row); err != nil {
					return err
				}
			}
		}
		if err := kw.Flush(); err != nil {
			return err
		}
	}
	printTally(tally)
	return nil
}

func printTally(tally map[string]int) {
	keys := make([]string, 0, len(tally))
	total := 0
	for k, v := range tally {
		keys = append(keys, k)
		total += v
	}
	sort.Slice(keys, func(i, j int) bool { return tally[keys[i]] > tally[keys[j]] })
	fmt.Fprintf(os.Stderr, "report: %d containers\n", total)
	for _, k := range keys {
		fmt.Fprintf(os.Stderr, "  %6d  %s\n", tally[k], k)
	}
}

// readIdents reads container idents, accepting bare 26-char idents or JSON
// objects carrying an "ident" field (as emitted by `search` / `search --json`).
func readIdents(in io.Reader) ([]string, error) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var idents []string
	for sc.Scan() {
		if id := parseIdent(sc.Text()); id != "" {
			idents = append(idents, id)
		}
	}
	return idents, sc.Err()
}

func parseIdent(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "{") {
		var obj struct {
			Ident string `json:"ident"`
		}
		if err := json.Unmarshal([]byte(raw), &obj); err == nil && obj.Ident != "" {
			return obj.Ident
		}
		return ""
	}
	if len(raw) == 26 {
		return raw
	}
	return ""
}

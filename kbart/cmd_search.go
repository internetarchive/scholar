package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/internetarchive/scholar/kbart/internal/es"
	"github.com/spf13/cobra"
)

var (
	searchOut   string
	searchQuery string
	searchBatch int
	searchJSON  bool
)

var searchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search fatcat_container for candidate preserved containers",
	Long: "search runs the preservation-threshold query over the fatcat_container\n" +
		"Elasticsearch index and emits one candidate container per line: the base32\n" +
		"ident by default, or the full ES _source document with --json. It replaces\n" +
		"search_fatcat_containers.sh.",
	RunE: runSearch,
}

func init() {
	f := searchCmd.Flags()
	f.StringVarP(&searchOut, "out", "o", "", "write to this file instead of stdout")
	f.StringVar(&searchQuery, "query", es.ContainerQuery, "Elasticsearch query_string to run")
	f.IntVar(&searchBatch, "batch", 1000, "scroll page size")
	f.BoolVar(&searchJSON, "json", false, "emit full ES _source docs (JSONL) instead of idents")
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	out := os.Stdout
	if searchOut != "" {
		f, err := os.Create(searchOut)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	w := bufio.NewWriter(out)
	defer w.Flush()

	client := es.NewClient(esHost)
	n := 0
	err := client.ScrollContainers(searchQuery, searchBatch, func(h es.Hit) error {
		n++
		if searchJSON {
			if _, err := w.Write(h.Source); err != nil {
				return err
			}
			return w.WriteByte('\n')
		}
		_, err := fmt.Fprintln(w, h.Ident)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "search: %d candidate containers\n", n)
	return nil
}

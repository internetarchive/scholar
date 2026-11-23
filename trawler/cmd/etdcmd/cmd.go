package etdcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.archive.org/webgroup/scholar/trawler/etd"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	concurrency      int
	indexConcurrency int
	dryRun           bool
	indexOnly        bool
	limit            int
)

var Cmd = &cobra.Command{
	Use:   "etd-ingest [FILE.ndjson[.gz]]",
	Short: "Ingest theses and dissertations from an etd-fulltext ndjson",
	Long: `Read an etd-fulltext ndjson (one thesis per line, with wayback PDF links)
and get each into fatcat as a release with files, then into the search indices.

For each line: look the record's DOI, handle and Wikidata QID up in fatcat; fetch
each of its PDFs from wayback and look its sha1 up; then fill in the blanks of a
release found either way (by sha1 only when the titles agree), or create one.
Files get the wayback URL as a webarchive link.

Indexing (GROBID plus three ES indices) runs alongside in its own worker pool,
fed from a durable queue in the state DB, so it never holds up fatcat writes.
A line counts as finished only once it is indexed; an interrupted run picks up
where it stopped. Settings come from the [etd] config section; flags override.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if indexOnly {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := etd.Config{
			Source:                 viper.GetString("etd.source"),
			Concurrency:            orInt(concurrency, viper.GetInt("etd.concurrency"), 16),
			IndexConcurrency:       orInt(indexConcurrency, viper.GetInt("etd.index_concurrency"), 8),
			Threshold:              viper.GetFloat64("etd.title_similarity_threshold"),
			MaxPDFBytes:            viper.GetInt64("etd.max_pdf_bytes"),
			StateDB:                viper.GetString("etd.state_db"),
			LogDir:                 viper.GetString("etd.log_dir"),
			DryRun:                 dryRun,
			IndexOnly:              indexOnly,
			Limit:                  limit,
			LineAttempts:           orInt(0, viper.GetInt("etd.line_attempts"), 3),
			IndexAttempts:          orInt(0, viper.GetInt("etd.index_attempts"), 3),
			MaxConsecutiveFailures: orInt(0, viper.GetInt("etd.max_consecutive_failures"), 50),
			ProgressEvery:          30 * time.Second,
		}
		if len(args) == 1 {
			cfg.Path = args[0]
		}
		if cfg.Source == "" {
			return errors.New("etd.source must be set in config")
		}
		if cfg.Threshold <= 0 || cfg.Threshold > 1 {
			return fmt.Errorf("etd.title_similarity_threshold must be in (0, 1], got %v", cfg.Threshold)
		}
		if cfg.MaxPDFBytes <= 0 {
			cfg.MaxPDFBytes = 512 << 20
		}
		if cfg.StateDB == "" && !dryRun {
			return errors.New("etd.state_db must be set in config")
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := etd.Run(ctx, cfg)
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted; rerun to pick up where this stopped")
			return nil
		}
		return err
	},
}

func init() {
	Cmd.Flags().IntVar(&concurrency, "concurrency", 0, "metadata workers (default [etd].concurrency)")
	Cmd.Flags().IntVar(&indexConcurrency, "index-concurrency", 0, "index workers (default [etd].index_concurrency)")
	Cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"look up and fetch, but write nothing to fatcat, ES or the state DB; the outcome log says what would happen")
	Cmd.Flags().BoolVar(&indexOnly, "index-only", false,
		"read no input; requeue parked index jobs and drain the index queue")
	Cmd.Flags().IntVar(&limit, "limit", 0, "stop after handing this many unfinished lines to workers")
}

func orInt(flag, conf, fallback int) int {
	switch {
	case flag > 0:
		return flag
	case conf > 0:
		return conf
	}
	return fallback
}

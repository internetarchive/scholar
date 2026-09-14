package scrapecmd

import (
	"log"

	"git.archive.org/webgroup/scholar/trawler/scrape"
	"github.com/spf13/cobra"
)

var (
	s3key       string
	concurrency int
	taskQueue   string
)

var Cmd = &cobra.Command{
	Use:   "scrape",
	Short: "Push a list of URLs through SPN into the Wayback Machine",
	Long: `Walk a newline-delimited list of URLs stored in S3, crawling each one
with the same PDF crawler the daily workflow uses, so that whatever sits behind
each URL is captured in the Wayback Machine.

This does not create fatcat records or index anything. It is for piles of URLs
we only want archived.`,
}

func init() {
	// --list is per-command rather than persistent: starting a worker does not
	// name a list, and a required persistent flag would demand one anyway.
	for _, c := range []*cobra.Command{StartOneOffCmd, SetConcurrencyCmd} {
		c.Flags().StringVar(&s3key, "list", "",
			"s3 key of the URL list, one URL per line (format: bucket/key)")
		c.MarkFlagRequired("list")
	}

	StartOneOffCmd.Flags().IntVar(&concurrency, "concurrency", 0,
		"How many URLs to crawl at once. Defaults to scrape.concurrency from config.")
	StartOneOffCmd.Flags().StringVar(&taskQueue, "task-queue", "",
		"Override the task queue. Defaults to scrape.task_queue from config.")

	SetConcurrencyCmd.Flags().IntVar(&concurrency, "concurrency", 0,
		"How many URLs the running scrape should crawl at once. Takes effect on "+
			"the next batch it dispatches, not the ones already running.")
	SetConcurrencyCmd.MarkFlagRequired("concurrency")

	Cmd.AddCommand(StartOneOffCmd)
	Cmd.AddCommand(SetConcurrencyCmd)
	Cmd.AddCommand(StartWorkerCmd)
}

var StartOneOffCmd = &cobra.Command{
	Use:   "one-off",
	Short: "Kick off a scrape of one URL list",
	RunE: func(cmd *cobra.Command, args []string) error {
		return scrape.StartOneOff(scrape.ScrapeToWaybackInput{
			S3Key:       s3key,
			Concurrency: concurrency,
			TaskQueue:   taskQueue,
		})
	},
}

var SetConcurrencyCmd = &cobra.Command{
	Use:   "set-concurrency",
	Short: "Retune how many crawls a running scrape does at once",
	RunE: func(cmd *cobra.Command, args []string) error {
		return scrape.SetConcurrency(s3key, concurrency)
	},
}

var StartWorkerCmd = &cobra.Command{
	Use:   "start-worker",
	Short: "Start a Temporal worker for scrapes (needs internet access)",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Print("starting scrape worker")
		return scrape.StartWorker()
	},
}

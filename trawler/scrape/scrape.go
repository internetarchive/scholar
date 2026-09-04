// Package scrape runs a flat list of URLs through Save Page Now so that
// whatever sits behind them lands in the Wayback Machine.
//
// It is deliberately much smaller than the daily crawl: no upstream metadata
// API, no fatcat records, no GROBID, no search indexing. Read URLs, crawl them,
// count what happened. It exists for piles of URLs we simply want captured --
// for example the crawl-only half of an ETD ingest split, where getting a
// landing page or even a single chapter PDF into Wayback is the entire goal.
//
// PDFCrawler.Crawl is still the crawler, so a URL that does resolve to a PDF is
// reported as such; we just do not do anything with the bytes afterwards.
package scrape

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/s3"
	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SetConcurrencySignal retunes how many crawls run at once without restarting
// the workflow. A run over millions of URLs lasts long enough that "back off,
// SPN is unhappy" needs to be answerable while it is in flight.
const SetConcurrencySignal = "set-concurrency"

const (
	defaultConcurrency          = 4
	defaultMaxConcurrency       = 64
	defaultURLsPerCAN           = 2000
	defaultBatchSize            = 1000
	defaultChunkSize            = 102400
	defaultMaxConsecutiveErrors = 50
)

// ScrapeCounts is the running tally for a scrape. Crawl outcomes that are
// simply "no PDF at the other end" are normal and expected, so they are counted
// rather than treated as failures.
type ScrapeCounts struct {
	// URLs is how many URLs we dispatched a crawl for.
	URLs int
	// PDFFound is how many crawls ended on a PDF in Wayback.
	PDFFound int
	// NoPDF is how many crawls completed without reaching a PDF. In most of
	// these SPN still captured a page, so they are not wasted for our purposes.
	NoPDF int
	// Skipped is how many URLs never reached the network at all, because the
	// crawler rejected them up front (blocklist, unparsable URL).
	Skipped int
	// Errored is how many crawls exhausted their retries and were abandoned so
	// the run could continue.
	Errored int
	// FailReasons breaks NoPDF and Skipped down by CrawlResult.FailReason.
	FailReasons map[string]int
}

// Add merges two tallies. It is used to fold each activity result into the
// workflow's running total, including across ContinueAsNew.
func (c ScrapeCounts) Add(o ScrapeCounts) ScrapeCounts {
	out := ScrapeCounts{
		URLs:     c.URLs + o.URLs,
		PDFFound: c.PDFFound + o.PDFFound,
		NoPDF:    c.NoPDF + o.NoPDF,
		Skipped:  c.Skipped + o.Skipped,
		Errored:  c.Errored + o.Errored,
	}
	if len(c.FailReasons) > 0 || len(o.FailReasons) > 0 {
		out.FailReasons = make(map[string]int, len(c.FailReasons)+len(o.FailReasons))
		for k, v := range c.FailReasons {
			out.FailReasons[k] += v
		}
		for k, v := range o.FailReasons {
			out.FailReasons[k] += v
		}
	}
	return out
}

// skipReasons are the FailReason values PDFCrawler produces before it ever
// touches the network. Everything else means SPN was asked to fetch something.
var skipReasons = map[string]bool{
	"blocklist": true,
	"bad-url":   true,
}

// ScrapeToWaybackInput drives the workflow. Callers starting a fresh run set
// S3Key (and optionally Concurrency); every other field is either populated
// from config on the first run or carried across ContinueAsNew.
type ScrapeToWaybackInput struct {
	// S3Key points at a newline-delimited list of URLs, one per line. Blank
	// lines are ignored.
	S3Key string
	// Concurrency is how many crawls are in flight at once. Zero means "use the
	// configured default".
	Concurrency int
	// TaskQueue is the Temporal task queue for both activities.
	TaskQueue string

	// Offset is the byte position in the list to resume reading from.
	Offset int64
	// Counts accumulates across every run in the ContinueAsNew chain.
	Counts ScrapeCounts

	// The fields below are viper-sourced settings snapshotted once on the first
	// run (see the SideEffect in the workflow) and threaded across
	// ContinueAsNew, so a config change under a running chain cannot desync its
	// replay. A zero BatchSize means we have not captured them yet.
	MaxConcurrency       int
	URLsPerCAN           int
	BatchSize            int
	ChunkSize            int
	MaxConsecutiveErrors int
}

// scrapeConfig is the snapshot of viper-sourced settings taken via SideEffect.
type scrapeConfig struct {
	Concurrency          int
	MaxConcurrency       int
	URLsPerCAN           int
	BatchSize            int
	ChunkSize            int
	MaxConsecutiveErrors int
	TaskQueue            string
}

func nonZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// ScrapeToWaybackWorkflow walks a URL list and crawls each entry, keeping a
// bounded number of crawls in flight.
//
// Per-URL failures never fail the run: a list of millions of URLs will always
// contain dead hosts, and one of them must not strand the other millions. Only
// a sustained run of errors -- which means something systemic like bad SPN
// credentials, not a bad URL -- aborts the workflow.
func ScrapeToWaybackWorkflow(ctx workflow.Context, in ScrapeToWaybackInput) (ScrapeCounts, error) {
	l := workflow.GetLogger(ctx)

	if in.S3Key == "" {
		return in.Counts, errors.New("scrape: S3Key is required")
	}

	// Snapshot config once, on the first run of the chain. SideEffect records
	// the read in history so replays reuse the recorded values rather than
	// re-reading viper, which is what keeps an in-flight chain deterministic
	// when the deployed config changes underneath it.
	if in.BatchSize == 0 {
		var cfg scrapeConfig
		if err := workflow.SideEffect(ctx, func(workflow.Context) any {
			return scrapeConfig{
				Concurrency:          viper.GetInt("scrape.concurrency"),
				MaxConcurrency:       viper.GetInt("scrape.max_concurrency"),
				URLsPerCAN:           viper.GetInt("scrape.urls_per_can"),
				BatchSize:            viper.GetInt("scrape.batch_size"),
				ChunkSize:            viper.GetInt("scrape.chunk_size"),
				MaxConsecutiveErrors: viper.GetInt("scrape.max_consecutive_errors"),
				TaskQueue:            viper.GetString("scrape.task_queue"),
			}
		}).Get(&cfg); err != nil {
			return in.Counts, err
		}
		in.MaxConcurrency = nonZero(cfg.MaxConcurrency, defaultMaxConcurrency)
		in.URLsPerCAN = nonZero(cfg.URLsPerCAN, defaultURLsPerCAN)
		in.BatchSize = nonZero(cfg.BatchSize, defaultBatchSize)
		in.ChunkSize = nonZero(cfg.ChunkSize, defaultChunkSize)
		in.MaxConsecutiveErrors = nonZero(cfg.MaxConsecutiveErrors, defaultMaxConsecutiveErrors)
		if in.Concurrency <= 0 {
			in.Concurrency = nonZero(cfg.Concurrency, defaultConcurrency)
		}
		if in.TaskQueue == "" {
			in.TaskQueue = cfg.TaskQueue
		}
	}
	if in.Concurrency > in.MaxConcurrency {
		in.Concurrency = in.MaxConcurrency
	}

	readCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		TaskQueue:           in.TaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    5,
		},
	})

	// A single crawl can sit for a long time: SPN slot polling, a headless
	// browser capture, then a Wayback replay fetch. The heartbeat is what
	// distinguishes "still working" from "wedged", so the StartToClose budget is
	// generous while the heartbeat timeout is short.
	crawlCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 1 * time.Hour,
		HeartbeatTimeout:    5 * time.Minute,
		TaskQueue:           in.TaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    30 * time.Second,
			BackoffCoefficient: 1.5,
			MaximumInterval:    5 * time.Minute,
			MaximumAttempts:    4,
		},
	})

	logCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		TaskQueue:           in.TaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    1 * time.Minute,
			MaximumAttempts:    4,
		},
	})

	sigCh := workflow.GetSignalChannel(ctx, SetConcurrencySignal)

	sel := workflow.NewSelector(ctx)
	inFlight := 0
	consecutiveErrors := 0

	// Rows for URLs we gave up on. CrawlToWaybackActivity logs its own verdict,
	// but an abandoned URL never reached one, and it would be missing from the
	// log entirely if we did not record it here. Collected rather than written
	// from the selector callback, because blocking on an activity inside a
	// callback would stall the Select that is draining the window.
	var abandoned []Outcome

	flushAbandoned := func() error {
		for _, o := range abandoned {
			if err := workflow.ExecuteActivity(logCtx, RecordOutcomeActivity, o).
				Get(logCtx, nil); err != nil {
				return err
			}
		}
		abandoned = nil
		return nil
	}

	drain := func() {
		for inFlight > 0 {
			sel.Select(ctx)
		}
	}

	// dispatch starts a crawl and registers its completion handler. Results are
	// folded in as they land, in whatever order they land; the selector callback
	// is the only place Counts is mutated by a crawl.
	dispatch := func(u string) {
		f := workflow.ExecuteActivity(crawlCtx, CrawlToWaybackActivity, CrawlToWaybackInput{URL: u})
		inFlight++
		sel.AddFuture(f, func(f workflow.Future) {
			inFlight--
			var res CrawlToWaybackOutput
			if err := f.Get(ctx, &res); err != nil {
				// Retries are exhausted. Abandon this URL and keep going.
				in.Counts.URLs++
				in.Counts.Errored++
				consecutiveErrors++
				abandoned = append(abandoned, Outcome{
					SeedURL: u,
					Status:  StatusError,
					Reason:  err.Error(),
					At:      workflow.Now(ctx),
				})
				l.Warn("giving up on url", "url", u, "err", err)
				return
			}
			consecutiveErrors = 0
			in.Counts = in.Counts.Add(res.Counts())
		})
	}

	var urlsThisRun int
	stop := false

	for !stop {
		var batch ReadURLBatchOutput
		if err := workflow.ExecuteActivity(readCtx, ReadURLBatch, ReadURLBatchInput{
			S3Key:     in.S3Key,
			Offset:    in.Offset,
			MaxURLs:   in.BatchSize,
			ChunkSize: in.ChunkSize,
		}).Get(readCtx, &batch); err != nil {
			return in.Counts, err
		}

		for _, ref := range batch.URLs {
			// Apply any pending retune before deciding whether the window is
			// full. ReceiveAsync keeps this deterministic without a coroutine.
			var want int
			for sigCh.ReceiveAsync(&want) {
				if want > 0 && want <= in.MaxConcurrency {
					l.Info("concurrency retuned", "from", in.Concurrency, "to", want)
					in.Concurrency = want
				} else {
					l.Warn("ignoring out-of-range concurrency signal",
						"want", want, "max", in.MaxConcurrency)
				}
			}

			for inFlight >= in.Concurrency {
				sel.Select(ctx)
			}
			if consecutiveErrors >= in.MaxConsecutiveErrors {
				stop = true
				break
			}

			dispatch(ref.URL)
			urlsThisRun++
			// Resume just past the URL we last dispatched. Everything dispatched
			// is drained before ContinueAsNew below, so this neither re-crawls
			// nor skips.
			in.Offset = ref.End

			if urlsThisRun >= in.URLsPerCAN || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
				stop = true
				break
			}
		}

		// Flush once per batch so abandonment rows land promptly and the pending
		// slice stays bounded by the batch size.
		if err := flushAbandoned(); err != nil {
			return in.Counts, err
		}

		if stop {
			break
		}

		// A batch that yields no URLs and does not advance would spin forever.
		// That should be impossible, but this loop is the one place a bad read
		// could hang a run indefinitely rather than fail it.
		if len(batch.URLs) == 0 && !batch.EOF && batch.NextOffset == in.Offset {
			drain()
			if err := flushAbandoned(); err != nil {
				return in.Counts, err
			}
			return in.Counts, fmt.Errorf(
				"scrape: read of %s made no progress at offset %d", in.S3Key, in.Offset)
		}

		in.Offset = batch.NextOffset
		if batch.EOF {
			// Drain before reporting the final total.
			drain()
			if err := flushAbandoned(); err != nil {
				return in.Counts, err
			}
			l.Info("scrape complete", "s3key", in.S3Key, "counts", in.Counts)
			return in.Counts, nil
		}
	}

	// Quiesce the window so the offset we carry forward is fully accounted for,
	// and get every row on disk before this run hands over.
	drain()
	if err := flushAbandoned(); err != nil {
		return in.Counts, err
	}

	if consecutiveErrors >= in.MaxConsecutiveErrors {
		return in.Counts, fmt.Errorf(
			"scrape: %d consecutive crawl failures, aborting (last offset %d)",
			consecutiveErrors, in.Offset)
	}

	l.Info("continue-as-new", "s3key", in.S3Key, "offset", in.Offset, "urls", urlsThisRun)
	return in.Counts, workflow.NewContinueAsNewError(ctx, ScrapeToWaybackWorkflow, in)
}

// URLRef is one URL from the list plus the byte offset just past its trailing
// newline, which is where a resume should pick up.
type URLRef struct {
	URL string
	End int64
}

type ReadURLBatchInput struct {
	S3Key     string
	Offset    int64
	MaxURLs   int
	ChunkSize int
}

type ReadURLBatchOutput struct {
	URLs []URLRef
	// NextOffset is where to resume if every returned URL is consumed.
	NextOffset int64
	// EOF reports that the scan reached the end of the object.
	EOF bool
}

// ReadURLBatch range-reads a slice of the URL list and returns the URLs it
// found. Returning the URLs themselves -- rather than offsets for the crawl
// activity to re-read, as the daily and periodic workflows do -- costs one S3
// request per batch instead of one per URL. Over millions of URLs that is the
// difference between thousands of S3 reads and millions of them, and a URL is
// small enough to sit in workflow history comfortably.
func ReadURLBatch(ctx context.Context, in ReadURLBatchInput) (ReadURLBatchOutput, error) {
	if in.MaxURLs <= 0 || in.ChunkSize <= 0 {
		return ReadURLBatchOutput{NextOffset: in.Offset},
			fmt.Errorf("scrape: bad batch config (max=%d chunk=%d)", in.MaxURLs, in.ChunkSize)
	}

	obj, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return ReadURLBatchOutput{NextOffset: in.Offset}, err
	}
	defer obj.Close()

	out, err := scanURLs(in, obj)
	if err != nil {
		return out, fmt.Errorf("scrape: reading %s: %w", in.S3Key, err)
	}
	return out, nil
}

// scanURLs is the reading logic, split out from the activity so it can be
// tested without S3.
func scanURLs(in ReadURLBatchInput, r io.ReaderAt) (ReadURLBatchOutput, error) {
	out := ReadURLBatchOutput{NextOffset: in.Offset}

	pos := in.Offset
	var partial []byte

	// flush emits a trailing final line that has no newline terminator.
	flush := func() {
		if u := strings.TrimSpace(string(partial)); u != "" {
			out.URLs = append(out.URLs, URLRef{URL: u, End: pos})
		}
		partial = nil
	}

	for len(out.URLs) < in.MaxURLs {
		buf := make([]byte, in.ChunkSize)
		n, err := r.ReadAt(buf, pos)
		atEnd := errors.Is(err, io.EOF)
		if err != nil && !atEnd {
			return out, fmt.Errorf("range read at %d failed: %w", pos, err)
		}
		if n == 0 {
			if atEnd {
				flush()
				out.EOF = true
				out.NextOffset = pos
			}
			return out, nil
		}

		rest := buf[:n]
		for len(rest) > 0 && len(out.URLs) < in.MaxURLs {
			i := bytes.IndexByte(rest, '\n')
			if i < 0 {
				// Line spans past this chunk; carry it into the next read.
				partial = append(partial, rest...)
				pos += int64(len(rest))
				rest = nil
				break
			}
			line := append(partial, rest[:i]...)
			partial = nil
			pos += int64(i + 1)
			rest = rest[i+1:]
			if u := strings.TrimSpace(string(line)); u != "" {
				out.URLs = append(out.URLs, URLRef{URL: u, End: pos})
			}
		}
		out.NextOffset = pos

		// Only a read that hit the end of the object *and* whose buffer we fully
		// consumed proves there is nothing left. Filling MaxURLs mid-buffer does
		// not, so EOF stays false and the caller resumes from NextOffset.
		if atEnd && len(rest) == 0 {
			if len(out.URLs) < in.MaxURLs {
				flush()
				out.NextOffset = pos
				out.EOF = true
			}
			return out, nil
		}
	}

	return out, nil
}

type CrawlToWaybackInput struct {
	URL string
}

type CrawlToWaybackOutput struct {
	URL string
	// Success reports that the crawl ended on a PDF.
	Success bool
	// FailReason is PDFCrawler's reason when Success is false.
	FailReason string
	// SnapshotURL is the Wayback replay URL of the PDF, when there is one.
	SnapshotURL string
	Mimetype    string
	// Chain is the hop sequence the crawler followed, useful when diagnosing
	// why a repository's URLs are not resolving.
	Chain      []string
	Techniques []string
}

// Counts turns one crawl outcome into a tally the workflow can fold in.
func (o CrawlToWaybackOutput) Counts() ScrapeCounts {
	c := ScrapeCounts{URLs: 1}
	if o.Success {
		c.PDFFound = 1
		return c
	}
	reason := o.FailReason
	if reason == "" {
		reason = "unknown"
	}
	if skipReasons[reason] {
		c.Skipped = 1
	} else {
		c.NoPDF = 1
	}
	c.FailReasons = map[string]int{reason: 1}
	return c
}

// CrawlToWaybackActivity crawls one URL. Reaching a PDF is the good outcome,
// but it is not the only useful one: any hop SPN actually fetched is now in
// Wayback, which is the point of this workflow.
//
// A crawl that simply does not find a PDF returns a nil error -- that is data,
// not failure. Only transport-level trouble (SPN or CDX misbehaving) is
// returned as an error for Temporal to retry.
func CrawlToWaybackActivity(ctx context.Context, in CrawlToWaybackInput) (CrawlToWaybackOutput, error) {
	activity.RecordHeartbeat(ctx, "started")
	l := activity.GetLogger(ctx)

	out := CrawlToWaybackOutput{URL: in.URL}

	spnClient, err := spnclient.NewDefaultClient(spnclient.SPNConfig{
		AccessKey: viper.GetString("spn.access_key"),
		SecretKey: viper.GetString("spn.secret_key"),
		Endpoint:  viper.GetString("spn.endpoint"),
	})
	if err != nil {
		return out, fmt.Errorf("spn client creation failed: %w", err)
	}

	cdxClient := cdx.NewClient(cdx.Config{
		Auth:      viper.GetString("cdx.auth"),
		Endpoint:  viper.GetString("cdx.endpoint"),
		UserAgent: viper.GetString("cdx.user_agent"),
		Retries:   viper.GetInt("cdx.retries"),
		Backoff:   viper.GetDuration("cdx.backoff"),
	})

	crawler := crawling.PDFCrawler{
		SPNClient:       spnClient,
		CDXClient:       cdxClient,
		MaxHops:         viper.GetInt("scrape.max_hops"),
		UserAgent:       viper.GetString("crawling.user_agent"),
		WaybackEndpoint: viper.GetString("wayback.replay_endpoint"),
		SimpleGets:      viper.GetStringSlice("crawling.simple_get_list"),
		Blocklist:       viper.GetStringSlice("crawling.url_blocklist"),
		Logger:          slog.Default(),
		Heartbeater: func(msg string) {
			activity.RecordHeartbeat(ctx, msg)
		},
	}
	if crawler.MaxHops <= 0 {
		crawler.MaxHops = 8
	}

	res, err := crawler.Crawl(in.URL)

	// Crawl hands back the Wayback replay body on success and never closes it.
	// We do not want the bytes, but at this volume leaking a connection per URL
	// would exhaust the worker, so drain and close it before doing anything else.
	if res.Content != nil {
		if c, ok := res.Content.(io.Closer); ok {
			io.Copy(io.Discard, res.Content)
			c.Close()
		}
	}

	if err != nil {
		return out, fmt.Errorf("crawl of %q failed: %w", in.URL, err)
	}

	out.Success = res.Success
	out.FailReason = res.FailReason
	out.SnapshotURL = res.SnapshotUrl
	out.Mimetype = res.Mimetype
	out.Chain = res.Chain
	out.Techniques = res.Techniques

	if res.Success {
		l.Info("pdf captured", "url", in.URL, "snapshot", res.SnapshotUrl)
	} else {
		l.Info("no pdf", "url", in.URL, "reason", res.FailReason, "hops", len(res.Chain))
	}

	// Written here, at the one point we know this URL's verdict, and only for a
	// crawl that reached a verdict -- an attempt that errors returns above and
	// gets retried, so retries do not each leave a row. URLs whose retries run
	// out are logged by the workflow via RecordOutcomeActivity instead.
	//
	// A failure to write is returned rather than swallowed: the log is a
	// deliverable of this workflow, so an unwritable log dir is a broken
	// deployment we want to hear about immediately, not silently lose rows to.
	// The retry re-crawls, but a re-crawl inside the CDX freshness window is a
	// CDX lookup rather than another SPN capture.
	if err := appendOutcome(viper.GetString("scrape.log_dir"),
		activity.GetInfo(ctx).WorkflowExecution.ID,
		outcomeFor(in.URL, res, time.Now())); err != nil {
		return out, err
	}

	activity.RecordHeartbeat(ctx, "done")
	return out, nil
}

// RecordOutcomeActivity appends a single row to the outcome log. The workflow
// uses it for URLs it abandons after their crawl retries are exhausted, which
// are the only outcomes CrawlToWaybackActivity never gets to record itself.
func RecordOutcomeActivity(ctx context.Context, o Outcome) error {
	return appendOutcome(viper.GetString("scrape.log_dir"),
		activity.GetInfo(ctx).WorkflowExecution.ID, o)
}

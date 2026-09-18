package pdf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// Process isolation for the MuPDF passes.
//
// MuPDF segfaults on some malformed PDFs. That is a crash in C during cgo, so
// no Go error handling can catch it -- the process simply dies, taking every
// other PDF the worker had in flight with it, and the retry comes back to the
// same PDF and dies again. One bad file could end a run.
//
// So the passes run in a short-lived child process: a copy of this binary
// invoked as `trawler pdf-extract-worker <path>`. A segfault there is a
// non-zero wait status here, which is an ordinary error. The PDF loses its text
// and thumbnail; the run does not notice.
//
// Two things fall out of this for free. A PDF that makes MuPDF hang rather than
// crash -- the failure mode the knownBadSHA1 list was originally collected for
// -- is now bounded by a timeout we can actually enforce, because killing a
// child is possible in a way that interrupting a cgo call is not. And each PDF
// gets a fresh address space, so nothing a malformed file does to the library's
// state can reach the next one.
//
// The cost is one fork+exec per PDF. Against a GROBID call and a full document
// render that is noise, and it is paid only for PDFs that get as far as
// extraction.

// extractWorkerArg is the argv[1] that turns this binary into an extraction
// child. It is a top-level argument rather than a cobra subcommand on purpose:
// the child needs no config file, no sentry and no version banner, and its
// stdout is a data channel that must not have anything else written to it.
const extractWorkerArg = "pdf-extract-worker"

const (
	defaultExtractTimeout = 2 * time.Minute
	// stderrCap bounds how much of a chatty child's stderr we keep for the
	// error message. MuPDF is talkative about malformed files, and a Go runtime
	// fatal dumps every goroutine.
	stderrCap = 8 << 10
	// stderrReportLines is how much of that we put in an error message. The
	// interesting part of a crash is the first line or two.
	stderrReportLines = 4
	// waitDelay bounds how long we wait for a killed child's pipes to close.
	waitDelay = 5 * time.Second
)

// The child's exit contract, which is what the parent classifies on.
//
// Deciding "did it crash" by exit status rather than by inspecting signals is
// deliberate. A segfault inside cgo does not reliably reach us as a signalled
// wait status: the Go runtime installs its own handler, prints a goroutine dump
// and may exit(2) instead of re-raising, so a parent that keys on "killed by a
// signal" misses the exact case this was built for. What the parent actually
// needs to know is whether the child answered, and only the child can say that
// -- so it says it with an exit code, and every other way of stopping counts as
// having died.
const (
	// exitOK means both passes ran and their outcomes are on stdout.
	exitOK = 0
	// exitReported means the child failed for a reason it has already explained
	// on stderr, and stopped on purpose.
	exitReported = 1
)

// ExtractResult is what the MuPDF passes produced for one PDF. Either pass can
// fail on its own, and a crash can leave one filled in and the other not.
type ExtractResult struct {
	Text         []byte
	TextErr      string
	Thumbnail    []byte
	ThumbnailErr string
}

// ExtractCrashError means the extraction child died instead of answering.
//
// This is the case the whole arrangement exists for, so it is worth a type: a
// PDF that crashes MuPDF will crash it again, which is useful to know, and it
// is the one extraction failure that says nothing at all about what the text or
// thumbnail would have been.
type ExtractCrashError struct {
	// Status is the wait status, e.g. "signal: segmentation fault".
	Status string
	// TimedOut reports that we killed it for taking too long, rather than it
	// dying on its own.
	TimedOut bool
	Stderr   string
}

func (e *ExtractCrashError) Error() string {
	what := "crashed"
	if e.TimedOut {
		what = "timed out"
	}
	msg := fmt.Sprintf("pdf extraction child %s (%s)", what, e.Status)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// pdfExtractor produces the MuPDF derivatives for a PDF on disk.
type pdfExtractor interface {
	extract(ctx context.Context, pdfPath string) (ExtractResult, error)
}

// newExtractor picks how the MuPDF passes run.
//
// pdf.extract_in_process is a rollback switch, not a tuning knob: it puts the
// passes back in this process, where a malformed PDF can take the worker down.
// It exists because "spawning a child per PDF turns out to be a problem in
// production" is a thing that can happen at 2am, and the alternative to a
// config flag then is a deploy. The in-process code is the same code the child
// runs, so it cannot rot while the flag is off.
func newExtractor() pdfExtractor {
	if viper.GetBool("pdf.extract_in_process") {
		return inProcessExtractor{}
	}

	timeout := viper.GetDuration("pdf.extract_timeout")
	if timeout <= 0 {
		timeout = defaultExtractTimeout
	}
	return &childExtractor{timeout: timeout}
}

// childExtractor runs the passes in a subprocess.
type childExtractor struct {
	timeout time.Duration
	// command builds the child. Left nil it re-executes this binary; a test
	// substitutes its own.
	command func(ctx context.Context, pdfPath string) *exec.Cmd
}

// selfPath is this executable, resolved once. A worker re-executes itself
// thousands of times, and the answer cannot change under a running process.
var selfPath = sync.OnceValues(os.Executable)

func (c *childExtractor) build(ctx context.Context, pdfPath string) (*exec.Cmd, error) {
	if c.command != nil {
		return c.command(ctx, pdfPath), nil
	}
	exe, err := selfPath()
	if err != nil {
		return nil, fmt.Errorf("could not find own executable: %w", err)
	}
	return exec.CommandContext(ctx, exe, extractWorkerArg, pdfPath), nil
}

func (c *childExtractor) extract(ctx context.Context, pdfPath string) (ExtractResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd, err := c.build(ctx, pdfPath)
	if err != nil {
		return ExtractResult{}, err
	}

	var stdout bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay

	runErr := cmd.Run()

	// Parse whatever arrived before deciding what the exit status means. A child
	// that delivered its text and then crashed rendering page 0 has given us
	// something worth keeping, and throwing it away because the process died
	// would waste the one pass that worked.
	res, parseErr := parseExtractChunks(stdout.Bytes())

	if runErr != nil {
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)

		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			// Never started, or we could not reap it. Not a verdict on the PDF.
			if timedOut {
				return res, &ExtractCrashError{
					Status:   "killed after " + c.timeout.String(),
					TimedOut: true,
					Stderr:   stderr.head(stderrReportLines),
				}
			}
			return res, fmt.Errorf("could not run pdf extraction child: %w", runErr)
		}

		if exit.ExitCode() == exitReported && !timedOut {
			return res, fmt.Errorf("pdf extraction child exited %d: %s",
				exit.ExitCode(), stderr.head(stderrReportLines))
		}

		// Anything else: a signal, a Go runtime fatal, a timeout kill. It died.
		return res, &ExtractCrashError{
			Status:   exit.ProcessState.String(),
			TimedOut: timedOut,
			Stderr:   stderr.head(stderrReportLines),
		}
	}

	return res, parseErr
}

// inProcessExtractor runs the passes here, in this address space. See
// newExtractor for when that is chosen and what it costs.
type inProcessExtractor struct{}

func (inProcessExtractor) extract(ctx context.Context, pdfPath string) (ExtractResult, error) {
	blob, err := os.ReadFile(pdfPath)
	if err != nil {
		return ExtractResult{}, err
	}

	var res ExtractResult
	runExtractPasses(ctx, blob, func(c extractChunk) { c.applyTo(&res) })
	return res, nil
}

// --- the wire between parent and child ------------------------------------

// extractChunk is one pass's outcome, one JSON object per line on the child's
// stdout.
//
// It is a stream rather than a single object at the end because the point of
// the child is that it might not reach the end. Each pass is written and
// flushed as soon as it finishes, so a crash costs the passes that had not run
// yet rather than everything.
type extractChunk struct {
	Pass string `json:"pass"`
	Data []byte `json:"data,omitempty"`
	Err  string `json:"err,omitempty"`
}

const (
	passText      = "text"
	passThumbnail = "thumbnail"
)

func (c extractChunk) applyTo(res *ExtractResult) {
	switch c.Pass {
	case passText:
		res.Text, res.TextErr = c.Data, c.Err
	case passThumbnail:
		res.Thumbnail, res.ThumbnailErr = c.Data, c.Err
	}
}

// runExtractPasses runs each MuPDF pass in order, handing each outcome to emit
// as soon as it is available.
//
// Text goes first deliberately: it is the pass whose output we actually index,
// and the one that has not crashed on us. A thumbnail render that dies then
// costs a thumbnail rather than the fulltext.
func runExtractPasses(ctx context.Context, blob []byte, emit func(extractChunk)) {
	text, err := extractText(ctx, blob)
	emit(chunkFor(passText, text, err))

	thumb, err := extractThumbnail(ctx, blob)
	emit(chunkFor(passThumbnail, thumb, err))
}

func chunkFor(pass string, data []byte, err error) extractChunk {
	c := extractChunk{Pass: pass}
	if err != nil {
		c.Err = err.Error()
		return c
	}
	c.Data = data
	return c
}

// parseExtractChunks reads the child's stdout. A truncated final line is
// expected -- that is what a crash mid-write looks like -- and is dropped
// rather than treated as corruption.
func parseExtractChunks(out []byte) (ExtractResult, error) {
	var res ExtractResult
	if len(out) == 0 {
		return res, nil
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	// A page of dense text can be large, and the whole document's text comes
	// through in one line.
	sc.Buffer(make([]byte, 64<<10), 64<<20)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var c extractChunk
		if err := json.Unmarshal(line, &c); err != nil {
			// Only the last line can be half-written, and we cannot tell here
			// whether this is it. Stopping is right either way: chunks are
			// ordered, so everything usable is already applied.
			return res, nil
		}
		c.applyTo(&res)
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("could not read pdf extraction child output: %w", err)
	}
	return res, nil
}

// --- the child ------------------------------------------------------------

// RunExtractWorkerIfInvoked turns this process into an extraction child and
// exits, when it was started as one. Otherwise it returns and the caller
// carries on with normal CLI dispatch.
//
// main calls this before cobra sees the arguments. The child must not read
// config, initialise sentry or log a version banner: it needs none of them, it
// would fail without a config file, and anything written to stdout that is not
// a chunk would corrupt the stream.
func RunExtractWorkerIfInvoked() {
	if len(os.Args) < 2 || os.Args[1] != extractWorkerArg {
		return
	}
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s %s <pdf-path>\n", os.Args[0], extractWorkerArg)
		os.Exit(exitReported)
	}
	os.Exit(runExtractWorker(os.Args[2], os.Stdout))
}

// runExtractWorker is the child's body, split out so it can be tested without
// spawning anything or exiting.
func runExtractWorker(pdfPath string, out *os.File) int {
	blob, err := os.ReadFile(pdfPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not read %s: %v\n", pdfPath, err)
		return exitReported
	}

	// os.File is unbuffered, so each write is a syscall and a chunk is on its
	// way the moment it is produced. That is what makes partial results
	// survive; a bufio.Writer here would lose them.
	emit := func(c extractChunk) {
		line, err := json.Marshal(c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not encode %s result: %v\n", c.Pass, err)
			return
		}
		out.Write(append(line, '\n'))
	}

	runExtractPasses(context.Background(), blob, emit)
	return exitOK
}

// --- helpers --------------------------------------------------------------

// cappedBuffer collects up to cap bytes and silently drops the rest, so a child
// that will not stop talking cannot grow the parent's heap.
type cappedBuffer struct {
	cap int
	buf bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.cap - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	// Report the whole write as accepted: the writer is a process we are not
	// trying to backpressure, and a short write would look like an I/O error.
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	return strings.TrimSpace(b.buf.String())
}

// head returns the first n lines, which for a crash is the part worth putting
// in a log line -- the rest is a goroutine dump.
func (b *cappedBuffer) head(n int) string {
	lines := strings.Split(b.String(), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return strings.Join(lines, " | ")
}

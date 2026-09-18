package pdf

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// The child is exercised for real: these tests re-execute the test binary as
// the extraction child, so the crash path actually segfaults a process and the
// parent actually reaps it. Mocking that out would test the mock -- the whole
// question here is what a dying process looks like from the other side.
//
// TestMain routes the re-executed copy into fakeChild.
func TestMain(m *testing.M) {
	if behaviour := os.Getenv("PDF_TEST_CHILD"); behaviour != "" {
		fakeChild(behaviour)
		return
	}
	os.Exit(m.Run())
}

// fakeChild stands in for the extraction worker, behaving however the parent
// asked it to.
func fakeChild(behaviour string) {
	emit := func(c extractChunk) {
		line, _ := json.Marshal(c)
		os.Stdout.Write(append(line, '\n'))
	}

	switch behaviour {
	case "ok":
		emit(extractChunk{Pass: passText, Data: []byte("page one\fpage two")})
		emit(extractChunk{Pass: passThumbnail, Data: []byte(strings.Repeat("j", 400))})
		os.Exit(0)

	case "text-then-crash":
		// The production shape: text comes back, then MuPDF dies rendering
		// page 0. The text must survive.
		emit(extractChunk{Pass: passText, Data: []byte("fulltext we should keep")})
		os.Stderr.WriteString("mupdf: cannot draw page\n")
		crash()

	case "crash-immediately":
		crash()

	case "hard-kill":
		// SIGKILL cannot be handled, so this process really is killed by a
		// signal -- the other shape a dying child comes back as.
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
		time.Sleep(time.Minute)

	case "runtime-fatal":
		// What a Go binary does when cgo faults: the runtime handles the
		// signal, dumps every goroutine to stderr and exits 2. A parent that
		// keyed on "killed by a signal" would miss exactly this.
		os.Stderr.WriteString("SIGSEGV: segmentation violation\nPC=0x1347047 m=13 sigcode=1\n" +
			strings.Repeat("goroutine dump line\n", 500))
		os.Exit(2)

	case "partial-line":
		// A crash part way through writing a chunk.
		emit(extractChunk{Pass: passText, Data: []byte("kept")})
		os.Stdout.WriteString(`{"pass":"thumbnail","dat`)
		crash()

	case "pass-error":
		emit(extractChunk{Pass: passText, Err: "fitz open: not a pdf"})
		emit(extractChunk{Pass: passThumbnail, Err: "pdf has no pages"})
		os.Exit(0)

	case "bad-exit":
		os.Stderr.WriteString("could not read /nope: no such file\n")
		os.Exit(1)

	case "hang":
		time.Sleep(5 * time.Minute)
		os.Exit(0)

	case "noisy":
		os.Stderr.WriteString(strings.Repeat("x", 64<<10))
		emit(extractChunk{Pass: passText, Data: []byte("fine")})
		os.Exit(0)

	default:
		os.Exit(3)
	}
}

// crash kills this process with SIGSEGV, which is what MuPDF does to us.
func crash() {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		os.Exit(4)
	}
	p.Signal(syscall.SIGSEGV)
	time.Sleep(time.Minute) // the signal arrives first
}

// childFor builds an extractor that re-executes the test binary as the child.
func childFor(t *testing.T, behaviour string, timeout time.Duration) *childExtractor {
	t.Helper()
	return &childExtractor{
		timeout: timeout,
		command: func(ctx context.Context, pdfPath string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestMain", pdfPath)
			cmd.Env = append(os.Environ(), "PDF_TEST_CHILD="+behaviour)
			return cmd
		},
	}
}

// aPDF writes a file for the child to be pointed at. Its contents do not matter
// -- the fake child never reads it -- but the path has to exist.
func aPDF(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.pdf")
	require.NoError(t, os.WriteFile(path, []byte("%PDF-1.7\n"), 0o644))
	return path
}

func Test_childExtractor_success(t *testing.T) {
	res, err := childFor(t, "ok", time.Minute).extract(context.Background(), aPDF(t))
	require.NoError(t, err)
	require.Equal(t, "page one\fpage two", string(res.Text))
	require.Len(t, res.Thumbnail, 400)
	require.Empty(t, res.TextErr)
	require.Empty(t, res.ThumbnailErr)
}

// The reason the child streams a chunk per pass instead of one object at the
// end: this is the production failure, and the fulltext is the output we
// actually index.
func Test_childExtractor_keepsTextWhenTheChildCrashesAfterIt(t *testing.T) {
	res, err := childFor(t, "text-then-crash", time.Minute).extract(context.Background(), aPDF(t))

	var crash *ExtractCrashError
	require.ErrorAs(t, err, &crash)
	require.False(t, crash.TimedOut)
	require.NotEmpty(t, crash.Status)
	require.Contains(t, crash.Stderr, "cannot draw page")

	require.Equal(t, "fulltext we should keep", string(res.Text),
		"the text arrived before the crash and must not be discarded with it")
	require.Empty(t, res.Thumbnail)
}

// A dying child is an error out here rather than the end of this process. That
// is the whole point, and it has to hold however the child dies: killed by a
// signal, or caught by the Go runtime which dumps and exits 2.
func Test_childExtractor_everyKindOfDeathIsACrash(t *testing.T) {
	for _, behaviour := range []string{"crash-immediately", "hard-kill", "runtime-fatal"} {
		t.Run(behaviour, func(t *testing.T) {
			res, err := childFor(t, behaviour, time.Minute).extract(context.Background(), aPDF(t))

			var crash *ExtractCrashError
			require.ErrorAs(t, err, &crash, "a child that did not answer must read as a crash")
			require.False(t, crash.TimedOut)
			require.NotEmpty(t, crash.Status)
			require.Empty(t, res.Text)
			require.Empty(t, res.Thumbnail)
		})
	}
}

// A goroutine dump must not end up inline in a log line.
func Test_childExtractor_trimsACrashDumpForReporting(t *testing.T) {
	_, err := childFor(t, "runtime-fatal", time.Minute).extract(context.Background(), aPDF(t))

	var crash *ExtractCrashError
	require.ErrorAs(t, err, &crash)
	require.Contains(t, crash.Stderr, "segmentation violation",
		"the useful first line has to survive")
	require.Less(t, len(crash.Stderr), 400, "the whole dump leaked into the message")
}

// A chunk cut in half by a crash is expected, not corruption: drop it and keep
// the complete ones.
func Test_childExtractor_toleratesATruncatedChunk(t *testing.T) {
	res, err := childFor(t, "partial-line", time.Minute).extract(context.Background(), aPDF(t))

	var crash *ExtractCrashError
	require.ErrorAs(t, err, &crash)
	require.Equal(t, "kept", string(res.Text))
	require.Empty(t, res.Thumbnail)
}

// A pass that fails cleanly is not a crash, and each pass reports for itself.
func Test_childExtractor_perPassErrors(t *testing.T) {
	res, err := childFor(t, "pass-error", time.Minute).extract(context.Background(), aPDF(t))
	require.NoError(t, err)
	require.Contains(t, res.TextErr, "not a pdf")
	require.Contains(t, res.ThumbnailErr, "no pages")
}

func Test_childExtractor_nonZeroExitIsNotACrash(t *testing.T) {
	_, err := childFor(t, "bad-exit", time.Minute).extract(context.Background(), aPDF(t))
	require.Error(t, err)

	var crash *ExtractCrashError
	require.False(t, errors.As(err, &crash), "a clean non-zero exit is not a crash")
	require.Contains(t, err.Error(), "exited 1")
	require.Contains(t, err.Error(), "no such file")
}

// The failure mode knownBadSHA1 was originally collected for is a hang, not a
// crash. Interrupting a cgo call is impossible; killing a child is not.
func Test_childExtractor_hangIsKilled(t *testing.T) {
	start := time.Now()
	_, err := childFor(t, "hang", 300*time.Millisecond).extract(context.Background(), aPDF(t))

	var crash *ExtractCrashError
	require.ErrorAs(t, err, &crash)
	require.True(t, crash.TimedOut)
	require.Less(t, time.Since(start), 30*time.Second, "the timeout did not fire")
}

// A cancelled caller stops the child too, rather than leaving it running.
func Test_childExtractor_honoursCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := childFor(t, "hang", time.Minute).extract(ctx, aPDF(t))
	require.Error(t, err)
	require.Less(t, time.Since(start), 30*time.Second)
}

// A child that will not stop talking must not grow the parent's heap.
func Test_childExtractor_boundsChildStderr(t *testing.T) {
	res, err := childFor(t, "noisy", time.Minute).extract(context.Background(), aPDF(t))
	require.NoError(t, err)
	require.Equal(t, "fine", string(res.Text))
}

func Test_cappedBuffer(t *testing.T) {
	b := &cappedBuffer{cap: 10}
	n, err := b.Write([]byte(strings.Repeat("a", 100)))
	require.NoError(t, err)
	require.Equal(t, 100, n, "a short write would look like an I/O error to the child")
	require.Len(t, b.String(), 10)
}

// --- the child's own body -------------------------------------------------

// runExtractWorker is what the real child runs; here it is driven directly so
// the stream format is pinned without spawning anything.
func Test_runExtractWorker_unreadableInput(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "out")
	require.NoError(t, err)
	defer out.Close()

	require.Equal(t, 1, runExtractWorker(filepath.Join(t.TempDir(), "nope.pdf"), out))
}

func Test_parseExtractChunks(t *testing.T) {
	line := func(c extractChunk) string {
		b, err := json.Marshal(c)
		require.NoError(t, err)
		return string(b) + "\n"
	}

	res, err := parseExtractChunks([]byte(
		line(extractChunk{Pass: passText, Data: []byte("hello")}) +
			line(extractChunk{Pass: passThumbnail, Err: "boom"})))
	require.NoError(t, err)
	require.Equal(t, "hello", string(res.Text))
	require.Equal(t, "boom", res.ThumbnailErr)

	// Nothing at all is what a child that died before its first write leaves.
	res, err = parseExtractChunks(nil)
	require.NoError(t, err)
	require.Empty(t, res.Text)

	// An unknown pass name is ignored rather than fatal, so a future pass can be
	// added to the child without the parent rejecting the stream.
	res, err = parseExtractChunks([]byte(line(extractChunk{Pass: "metadata", Data: []byte("x")}) +
		line(extractChunk{Pass: passText, Data: []byte("still here")})))
	require.NoError(t, err)
	require.Equal(t, "still here", string(res.Text))
}

func Test_extractChunk_roundTripsBinaryData(t *testing.T) {
	// Thumbnails are JPEG bytes, so the encoding has to survive arbitrary
	// binary rather than just text.
	jpegish := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
	b, err := json.Marshal(extractChunk{Pass: passThumbnail, Data: jpegish})
	require.NoError(t, err)

	res, err := parseExtractChunks(append(b, '\n'))
	require.NoError(t, err)
	require.Equal(t, jpegish, res.Thumbnail)
}

// --- choosing an extractor ------------------------------------------------

func Test_newExtractor(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	require.IsType(t, &childExtractor{}, newExtractor(),
		"isolation has to be the default; opting in would mean nobody does")
	require.Equal(t, defaultExtractTimeout, newExtractor().(*childExtractor).timeout)

	viper.Set("pdf.extract_timeout", "45s")
	require.Equal(t, 45*time.Second, newExtractor().(*childExtractor).timeout)

	// The 2am rollback switch.
	viper.Set("pdf.extract_in_process", true)
	require.IsType(t, inProcessExtractor{}, newExtractor())
}

// The in-process path runs the same passes the child does, so it has to behave
// the same for a caller: per-pass errors, no error from extract itself.
func Test_inProcessExtractor_reportsPerPassErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.pdf")
	require.NoError(t, os.WriteFile(path, []byte("not a pdf at all"), 0o644))

	res, err := inProcessExtractor{}.extract(context.Background(), path)
	require.NoError(t, err, "a PDF neither pass can read is not an extractor failure")
	require.NotEmpty(t, res.TextErr)
	require.NotEmpty(t, res.ThumbnailErr)
	require.Empty(t, res.Text)
	require.Empty(t, res.Thumbnail)
}

func Test_inProcessExtractor_unreadableFile(t *testing.T) {
	_, err := inProcessExtractor{}.extract(context.Background(), filepath.Join(t.TempDir(), "nope.pdf"))
	require.Error(t, err)
}

// A child that will not stop talking is also a child whose output we still
// want: the cap is on stderr, not on the result.
func Test_childExtractor_missingBinaryIsNotACrash(t *testing.T) {
	c := &childExtractor{
		timeout: time.Minute,
		command: func(ctx context.Context, pdfPath string) *exec.Cmd {
			return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "no-such-binary"), pdfPath)
		},
	}

	_, err := c.extract(context.Background(), aPDF(t))
	require.Error(t, err)

	var crash *ExtractCrashError
	require.False(t, errors.As(err, &crash),
		"a binary that never started is a deployment problem, not a bad PDF")
	require.Contains(t, err.Error(), "could not run pdf extraction child")
}

package httpclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func Test_newTransport_defaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tr := newTransport()

	// The whole point of the package: not Go's default of 2.
	if tr.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost: got %d, want %d",
			tr.MaxIdleConnsPerHost, defaultMaxIdleConnsPerHost)
	}
	if tr.MaxIdleConns != defaultMaxIdleConns {
		t.Errorf("MaxIdleConns: got %d, want %d", tr.MaxIdleConns, defaultMaxIdleConns)
	}
	if tr.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout: got %s, want %s",
			tr.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}
	// Cloned from DefaultTransport, so proxy and dial settings come along.
	if tr.Proxy == nil {
		t.Error("Proxy is nil; transport was not cloned from DefaultTransport")
	}
	// An unset MaxConnsPerHost is deliberate: it would throttle by blocking in
	// Do, invisibly.
	if tr.MaxConnsPerHost != 0 {
		t.Errorf("MaxConnsPerHost: got %d, want 0 (unlimited)", tr.MaxConnsPerHost)
	}
}

func Test_newTransport_config(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("http.max_idle_conns_per_host", 256)
	viper.Set("http.max_idle_conns", 1024)
	viper.Set("http.idle_conn_timeout", "45s")
	viper.Set("http.response_header_timeout", "30s")

	tr := newTransport()

	if tr.MaxIdleConnsPerHost != 256 {
		t.Errorf("MaxIdleConnsPerHost: got %d, want 256", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxIdleConns != 1024 {
		t.Errorf("MaxIdleConns: got %d, want 1024", tr.MaxIdleConns)
	}
	if tr.IdleConnTimeout != 45*time.Second {
		t.Errorf("IdleConnTimeout: got %s, want 45s", tr.IdleConnTimeout)
	}
	if tr.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("ResponseHeaderTimeout: got %s, want 30s", tr.ResponseHeaderTimeout)
	}
}

func Test_newTransport_explicitZeroDisables(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	// Set to zero means off. Absent means "use the default", which is why this
	// is not just a GetDuration.
	viper.Set("http.response_header_timeout", 0)
	if got := newTransport().ResponseHeaderTimeout; got != 0 {
		t.Errorf("explicit zero should disable the timeout, got %s", got)
	}

	// A nonsense pool size falls back rather than crippling the pool.
	viper.Set("http.max_idle_conns_per_host", 0)
	if got := newTransport().MaxIdleConnsPerHost; got != defaultMaxIdleConnsPerHost {
		t.Errorf("zero pool size: got %d, want default %d", got, defaultMaxIdleConnsPerHost)
	}
}

func Test_clientsSharePool(t *testing.T) {
	// Sharing one transport is what makes the pool a pool; two clients each
	// with their own would be the bug this package exists to fix.
	if Shared().Transport != NoRedirect().Transport {
		t.Error("Shared and NoRedirect do not share a transport")
	}
	if Shared().Timeout != 0 {
		t.Errorf("Shared should set no whole-request timeout, got %s", Shared().Timeout)
	}

	if NoRedirect().CheckRedirect == nil {
		t.Fatal("NoRedirect has no CheckRedirect")
	}
	if err := NoRedirect().CheckRedirect(&http.Request{}, nil); err != http.ErrUseLastResponse {
		t.Errorf("NoRedirect should stop at the redirect, got %v", err)
	}
	if Shared().CheckRedirect != nil {
		t.Error("Shared should follow redirects")
	}
}

// blockingBody is a response body that has stopped arriving: Read blocks until
// the request is cancelled, which is exactly how a silent server looks from
// this side of the connection.
type blockingBody struct {
	release chan struct{}
	closes  atomic.Int32
}

func newBlockingBody() *blockingBody {
	return &blockingBody{release: make(chan struct{})}
}

func (b *blockingBody) Read(p []byte) (int, error) {
	<-b.release
	// What the transport hands back once the context is cancelled.
	return 0, context.Canceled
}

func (b *blockingBody) Close() error {
	b.closes.Add(1)
	return nil
}

// tickingBody returns a byte per read, each taking interval to arrive: slow,
// but never actually stalled.
type tickingBody struct {
	interval time.Duration
	reads    int
	max      int
}

func (b *tickingBody) Read(p []byte) (int, error) {
	if b.reads >= b.max {
		return 0, io.EOF
	}
	time.Sleep(b.interval)
	b.reads++
	p[0] = 'x'
	return 1, nil
}

func (b *tickingBody) Close() error { return nil }

// countingCancel stands in for a request's CancelFunc, releasing the body the
// way cancelling a real request would.
func countingCancel(body *blockingBody) (context.CancelFunc, *atomic.Int32) {
	var calls atomic.Int32
	var once sync.Once
	return func() {
		calls.Add(1)
		if body != nil {
			once.Do(func() { close(body.release) })
		}
	}, &calls
}

func Test_StallGuard_abortsStalledRead(t *testing.T) {
	body := newBlockingBody()
	cancel, calls := countingCancel(body)

	guard := StallGuard(body, cancel, 20*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := guard.Read(make([]byte, 8))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled read returned no error")
		}
		// The operator-facing message has to name the real problem, not just
		// pass along "context canceled".
		if !strings.Contains(err.Error(), "stalled") {
			t.Errorf("error should say the read stalled, got %q", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled read was never interrupted")
	}

	if calls.Load() == 0 {
		t.Error("watchdog did not cancel the request")
	}
}

func Test_StallGuard_progressResetsTheClock(t *testing.T) {
	// Five reads of 20ms is 100ms of reading, well past the timeout in total
	// but never stalled for it. A total-duration bound would kill this; an
	// idle bound must not.
	cancel, calls := countingCancel(nil)
	guard := StallGuard(&tickingBody{interval: 20 * time.Millisecond, max: 5},
		cancel, 200*time.Millisecond)

	n, err := io.Copy(io.Discard, guard)
	if err != nil {
		t.Fatalf("slow but steady body failed: %s", err)
	}
	if n != 5 {
		t.Errorf("read %d bytes, want 5", n)
	}
	if calls.Load() != 0 {
		t.Errorf("watchdog fired on a body that was making progress (%d calls)", calls.Load())
	}
}

func Test_StallGuard_closeStopsTheWatchdog(t *testing.T) {
	body := newBlockingBody()
	cancel, calls := countingCancel(body)

	guard := StallGuard(body, cancel, 50*time.Millisecond)
	if err := guard.Close(); err != nil {
		t.Fatalf("close: %s", err)
	}

	// Long enough that an unstopped timer would have fired by now.
	time.Sleep(200 * time.Millisecond)

	if got := calls.Load(); got != 1 {
		t.Errorf("cancel called %d times after close, want exactly 1", got)
	}
	if got := body.closes.Load(); got != 1 {
		t.Errorf("underlying body closed %d times, want 1", got)
	}

	// Close is what releases the request's context, so a second one must not
	// cancel twice over.
	if err := guard.Close(); err != nil {
		t.Fatalf("second close: %s", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("cancel called %d times after two closes, want 1", got)
	}
}

func Test_StallGuard_zeroTimeoutDisablesWatchdog(t *testing.T) {
	cancel, calls := countingCancel(nil)
	guard := StallGuard(&tickingBody{interval: time.Millisecond, max: 3}, cancel, 0)

	if _, err := io.Copy(io.Discard, guard); err != nil {
		t.Fatalf("read with watchdog disabled: %s", err)
	}
	if calls.Load() != 0 {
		t.Error("watchdog fired despite being disabled")
	}

	// Disabled watchdog or not, the context still has to be released.
	if err := guard.Close(); err != nil {
		t.Fatalf("close: %s", err)
	}
	if calls.Load() != 1 {
		t.Errorf("close should cancel exactly once, got %d", calls.Load())
	}
}

package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// DefaultMaxRetries is how many times a model request is retried after a
// 429, a 5xx, or a connection error (Python `retries.max_retries`).
const DefaultMaxRetries = 2

// ErrStreamIdle means a streamed response sent nothing for StreamIdleTimeout.
var ErrStreamIdle = errors.New("model stream idle timeout")

var (
	// StreamIdleTimeout aborts a streamed response that sends no bytes for
	// this long, including the wait for headers. Reasoning models can stay
	// silent for minutes, so it is generous.
	StreamIdleTimeout = 5 * time.Minute
	// responseHeaderTimeout bounds non-streamed requests, whose headers only
	// arrive once the whole completion is ready.
	responseHeaderTimeout = 10 * time.Minute
	retryBaseDelay        = time.Second
	maxRetryDelay         = 30 * time.Second
)

type doer interface {
	Do(req *http.Request) (*http.Response, error)
}

var (
	sharedClientOnce sync.Once
	sharedClient     *http.Client
)

// modelHTTPClient is shared by every model so connections are reused.
func modelHTTPClient() *http.Client {
	sharedClientOnce.Do(func() {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = responseHeaderTimeout
		sharedClient = &http.Client{Transport: t}
	})
	return sharedClient
}

// retryDoer retries retryable failures with exponential backoff. Retries
// happen before any response body is handed back, so nothing has streamed.
type retryDoer struct {
	base       doer
	maxRetries int
}

func (r retryDoer) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if req.GetBody == nil {
				return nil, errors.New("model request cannot be retried: body is not replayable")
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req = req.Clone(ctx)
			req.Body = body
		}
		resp, err := r.base.Do(req)
		if attempt >= r.maxRetries || !retryable(ctx, resp, err) {
			return resp, err
		}
		delay := retryDelay(attempt, resp)
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctxError(ctx)
		}
	}
}

func retryable(ctx context.Context, resp *http.Response, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		return true
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
}

// retryDelay doubles from retryBaseDelay, or follows a Retry-After in seconds,
// capped at maxRetryDelay.
func retryDelay(attempt int, resp *http.Response) time.Duration {
	delay := retryBaseDelay << attempt
	if resp != nil {
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs >= 0 {
			delay = time.Duration(secs) * time.Second
		}
	}
	return min(delay, maxRetryDelay)
}

// ctxError reports ErrStreamIdle when the watchdog cancelled ctx.
func ctxError(ctx context.Context) error {
	if cause := context.Cause(ctx); errors.Is(cause, ErrStreamIdle) {
		return fmt.Errorf("%w after %s", ErrStreamIdle, StreamIdleTimeout)
	}
	return ctx.Err()
}

// idleWatch cancels a request when no bytes arrive for StreamIdleTimeout.
type idleWatch struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
}

func startIdleWatch(parent context.Context) *idleWatch {
	ctx, cancel := context.WithCancelCause(parent)
	w := &idleWatch{ctx: ctx, cancel: cancel}
	w.timer = time.AfterFunc(StreamIdleTimeout, func() { cancel(ErrStreamIdle) })
	return w
}

func (w *idleWatch) stop() {
	w.timer.Stop()
	w.cancel(nil)
}

// watchedBody resets the idle timer on every read and stops it on Close.
type watchedBody struct {
	r     io.ReadCloser
	watch *idleWatch
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.watch.timer.Reset(StreamIdleTimeout)
	}
	if err != nil && err != io.EOF && b.watch.ctx.Err() != nil {
		err = ctxError(b.watch.ctx)
	}
	return n, err
}

func (b *watchedBody) Close() error {
	b.watch.stop()
	return b.r.Close()
}

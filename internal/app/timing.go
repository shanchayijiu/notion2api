package app

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// requestTimer records per-request latency milestones. Phase 2 requires more
// than a total duration: TTFB/TTFT and last-token timing are needed to tell
// upstream slowness apart from gateway buffering.
type requestTimer struct {
	RequestID string
	Start     time.Time

	mu         sync.Mutex
	firstToken time.Time
	lastToken  time.Time
	model      string
	account    string
	retries    int
	streamErr  bool
}

type requestTimerContextKey struct{}

func newRequestTimer(requestID string) *requestTimer {
	return &requestTimer{RequestID: requestID, Start: time.Now()}
}

func withRequestTimer(r *http.Request, timer *requestTimer) *http.Request {
	if r == nil || timer == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), requestTimerContextKey{}, timer))
}

func requestTimerFromRequest(r *http.Request) *requestTimer {
	if r == nil {
		return nil
	}
	timer, _ := r.Context().Value(requestTimerContextKey{}).(*requestTimer)
	return timer
}

// MarkToken records the first emitted token (TTFT) and refreshes the
// last-token timestamp. Safe to call from the streaming goroutine.
func (t *requestTimer) MarkToken() {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	if t.firstToken.IsZero() {
		t.firstToken = now
	}
	t.lastToken = now
	t.mu.Unlock()
}

func (t *requestTimer) SetModel(model string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.model = strings.TrimSpace(model)
	t.mu.Unlock()
}

func (t *requestTimer) SetAccount(account string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.account = strings.TrimSpace(account)
	t.mu.Unlock()
}

func (t *requestTimer) AddRetry() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.retries++
	t.mu.Unlock()
}

func (t *requestTimer) MarkStreamError() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.streamErr = true
	t.mu.Unlock()
}

func (t *requestTimer) TTFT() time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstToken.IsZero() {
		return 0
	}
	return t.firstToken.Sub(t.Start)
}

// HasToken reports whether at least one token was emitted. Used so a
// sub-millisecond TTFT is still observed (Windows clock resolution can round it
// to zero).
func (t *requestTimer) HasToken() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.firstToken.IsZero()
}

type requestTimingSnapshot struct {
	RequestID string
	Model     string
	Account   string
	TTFT      time.Duration
	LastToken time.Duration
	Total     time.Duration
	Retries   int
	StreamErr bool
}

func (t *requestTimer) snapshot(total time.Duration) requestTimingSnapshot {
	if t == nil {
		return requestTimingSnapshot{Total: total}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	snap := requestTimingSnapshot{
		RequestID: t.RequestID,
		Model:     t.model,
		Account:   t.account,
		Total:     total,
		Retries:   t.retries,
		StreamErr: t.streamErr,
	}
	if !t.firstToken.IsZero() {
		snap.TTFT = t.firstToken.Sub(t.Start)
	}
	if !t.lastToken.IsZero() {
		snap.LastToken = t.lastToken.Sub(t.Start)
	}
	return snap
}

func logRequestCompletion(timer *requestTimer, r *http.Request, status int, elapsed time.Duration) {
	snap := timer.snapshot(elapsed)
	path := ""
	method := ""
	if r != nil {
		path = r.URL.Path
		method = r.Method
	}
	log.Printf("[req] id=%s method=%s path=%s status=%d model=%s account=%s ttft_ms=%d total_ms=%d retries=%d stream_error=%t",
		snap.RequestID,
		method,
		path,
		status,
		firstNonEmpty(snap.Model, "-"),
		firstNonEmpty(snap.Account, "-"),
		snap.TTFT.Milliseconds(),
		snap.Total.Milliseconds(),
		snap.Retries,
		snap.StreamErr,
	)
}

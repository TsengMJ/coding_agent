package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetries(t *testing.T) {
	t.Helper()
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
}

func shortIdle(t *testing.T, d time.Duration) {
	t.Helper()
	old := StreamIdleTimeout
	StreamIdleTimeout = d
	t.Cleanup(func() { StreamIdleTimeout = old })
}

// scriptedOpenAI answers each request with the next status; 200 sends textReply.
func scriptedOpenAI(t *testing.T, statuses ...int) (*OpenAIModel, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1)) - 1
		status := statuses[min(n, len(statuses)-1)]
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, textReply)
			return
		}
		_, _ = io.WriteString(w, `{"error":{"message":"try later"}}`)
	}))
	t.Cleanup(srv.Close)
	return NewOpenAIModel(ProviderConfig{Model: "gpt-test", APIKey: "sk-test", BaseURL: srv.URL}, false), &hits
}

func TestOpenAIRetriesServerErrors(t *testing.T) {
	fastRetries(t)
	m, hits := scriptedOpenAI(t, 503, 429, 200)
	got, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err != nil || got.Text() != "hello" {
		t.Fatalf("reply = %+v, %v", got, err)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestOpenAIGivesUpAfterMaxRetries(t *testing.T) {
	fastRetries(t)
	m, hits := scriptedOpenAI(t, 429)
	if _, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}); err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != DefaultMaxRetries+1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestOpenAIDoesNotRetryClientErrors(t *testing.T) {
	fastRetries(t)
	m, hits := scriptedOpenAI(t, 400, 200)
	if _, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}); err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestOpenAIMaxRetriesSetting(t *testing.T) {
	fastRetries(t)
	m, hits := scriptedOpenAI(t, 500)
	m.cfg.Settings = map[string]any{"max_retries": 0}
	if _, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}); err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestOpenAIStreamTimesOutWaitingForHeaders(t *testing.T) {
	shortIdle(t, 200*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	m := NewOpenAIModel(ProviderConfig{Model: "gpt-test", APIKey: "sk-test", BaseURL: srv.URL}, false)
	m.cfg.Settings = map[string]any{"max_retries": 0}
	var streamErr error
	for _, err := range m.Stream(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}) {
		if err != nil {
			streamErr = err
		}
	}
	if !errors.Is(streamErr, ErrStreamIdle) {
		t.Fatalf("err = %v", streamErr)
	}
}

func TestOpenAIStreamIdleTimeout(t *testing.T) {
	shortIdle(t, 200*time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hel"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	m := NewOpenAIModel(ProviderConfig{Model: "gpt-test", APIKey: "sk-test", BaseURL: srv.URL}, false)

	start := time.Now()
	var streamErr error
	for _, err := range m.Stream(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}) {
		if err != nil {
			streamErr = err
		}
	}
	if !errors.Is(streamErr, ErrStreamIdle) {
		t.Fatalf("err = %v", streamErr)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

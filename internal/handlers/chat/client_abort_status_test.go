package chat

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

// TestIsClientCanceledTypedClassification pins which errors count as a client
// abort, with and without a context that can answer the question itself.
//
// The request-log call site in usage.go passes a nil context, so the ctx.Err()
// branch cannot fire there: only the typed checks can recognise a hung-up
// client (#475). `context.DeadlineExceeded` stays an upstream failure, and an
// error whose text merely says "client closed" stays one too (#455).
func TestIsClientCanceledTypedClassification(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	urlErr := fmt.Errorf("Post %q: %w", "https://upstream.example/v1", fmt.Errorf("net/http: %w", context.Canceled))

	tests := []struct {
		name    string
		ctx     context.Context
		err     error
		want    bool
		comment string
	}{
		{"nil ctx, bare cancel", nil, context.Canceled, true, "the log call site's shape"},
		{"nil ctx, wrapped cancel", nil, fmt.Errorf("upstream request failed: %w", context.Canceled), true, "the forward error's shape"},
		{"nil ctx, url.Error cancel", nil, urlErr, true, "transport abort wrapped twice"},
		{"background ctx, wrapped cancel", context.Background(), fmt.Errorf("upstream request failed: %w", context.Canceled), true, "context has nothing to report yet"},
		{"nil ctx, upstreamError 499", nil, &upstreamError{StatusCode: StatusClientClosedRequest}, true, "already classified upstream"},
		{"nil ctx, deadline exceeded", nil, context.DeadlineExceeded, false, "a gateway timeout is not a client abort"},
		{"nil ctx, string-shaped client closed", nil, errors.New("upstream: client closed"), false, "text alone must not classify (#455)"},
		{"nil ctx, string-shaped broken pipe", nil, errors.New("write: broken pipe"), false, "text alone must not classify (#455)"},
		{"nil ctx, upstreamError 502", nil, &upstreamError{StatusCode: http.StatusBadGateway}, false, "an upstream failure stays an upstream failure"},
		{"nil ctx, plain error", nil, errors.New("boom"), false, "unknown failures are not aborts"},
		{"nil ctx, nil error", nil, nil, false, "no error is not an abort"},
		{"cancelled ctx, deadline exceeded", cancelled, context.DeadlineExceeded, true, "the request context already gave up"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClientCanceled(tt.ctx, tt.err); got != tt.want {
				t.Fatalf("isClientCanceled = %v, want %v (%s)", got, tt.want, tt.comment)
			}
		})
	}
}

// TestLogFailureRecordsClientAbortAs499 is the end-to-end guard the issue asked
// for: the persisted requestDetails row must carry 499 for a cancelled request
// and 502 for anything else that is not a client abort.
func TestLogFailureRecordsClientAbortAs499(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"wrapped context.Canceled", fmt.Errorf("upstream request failed: %w", context.Canceled), "499"},
		{"transport abort", fmt.Errorf("Post %q: %w", "https://upstream.example", context.Canceled), "499"},
		{"upstream 499", &upstreamError{StatusCode: StatusClientClosedRequest, Body: []byte(`{"error":{"message":"closed"}}`)}, "499"},
		{"upstream 429 keeps its status", &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"rate limited"}}`)}, "429"},
		{"deadline exceeded stays 502", context.DeadlineExceeded, "502"},
		{"string-shaped abort stays 502", errors.New("upstream: client closed"), "502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			handler := NewChatHandler(db.NewRepo(database))
			handler.LogFailure(
				&UsageLogInfo{Provider: "openai", Model: "gpt-test", ConnectionID: "conn-1", APIKey: "sk-secret", Endpoint: "/v1/chat/completions"},
				&translator.OpenAIUsage{PromptTokens: 7},
				tt.err,
				42,
				[]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
				nil,
			)
			var data string
			if err := database.QueryRow(`SELECT data FROM requestDetails WHERE status = 'error'`).Scan(&data); err != nil {
				t.Fatalf("read detail: %v", err)
			}
			var row struct {
				Response struct {
					Status int `json:"status"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(data), &row); err != nil {
				t.Fatalf("unmarshal detail: %v (%s)", err, data)
			}
			if got := strconv.Itoa(row.Response.Status); got != tt.wantCode {
				t.Fatalf("logged response.status = %s, want %s (%s)", got, tt.wantCode, data)
			}
		})
	}
}

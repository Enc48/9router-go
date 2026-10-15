package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// ForwardDeepSeekWeb routes a chat completion through chat.deepseek.com's web
// API using a browser-login credential — the port of OmniRoute's
// open-sse/executors/deepseek-web.ts.
//
// The turn is four upstream calls: exchange the pasted userToken for a
// short-lived access token, open a chat session, fetch and answer a proof of
// work, then stream the completion. Each can fail independently, and each
// failure has to surface as the status the router can act on — a 401 so the
// account is cooled down and rotated, a 429 so the client backs off — rather
// than as one opaque 502.
func ForwardDeepSeekWeb(w http.ResponseWriter, req *Request) error {
	var envelope deepSeekRequestEnvelope
	if err := json.Unmarshal(req.Body, &envelope); err != nil {
		return fmt.Errorf("deepseek-web: parse request: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	userToken := deepSeekUserToken(req.APIKey)
	if userToken == "" {
		return deepSeekFailure(http.StatusBadRequest,
			"deepseek-web needs a userToken — paste the value from chat.deepseek.com DevTools → Application → Local Storage → userToken")
	}
	if len(envelope.Messages) == 0 {
		return deepSeekFailure(http.StatusBadRequest, "deepseek-web received an empty messages array")
	}

	options := resolveDeepSeekModelOptions(envelope.Model, envelope)
	prompt := deepSeekMessagesToPrompt(envelope.Messages, deepSeekHistoryWindow(req.ConnData))
	if prompt == "" {
		return deepSeekFailure(http.StatusBadRequest, "deepseek-web received a request with no text content")
	}

	turn := newDeepSeekTurn(req.Client, deepSeekBaseURL(req.Config))

	accessToken, err := turn.acquireAccessToken(ctx, userToken)
	if err != nil {
		return deepSeekAuthFailure(err, userToken)
	}

	sessionID, err := turn.createSession(ctx, accessToken)
	if err != nil {
		return deepSeekAuthFailure(err, userToken)
	}

	response, err := turn.completion(ctx, accessToken, deepSeekCompletionRequest{
		ChatSessionID:   sessionID,
		ModelType:       options.modelType,
		Prompt:          prompt,
		RefFileIDs:      []string{},
		ThinkingEnabled: options.thinking,
		SearchEnabled:   options.search,
	})
	if err != nil {
		return deepSeekAuthFailure(err, userToken)
	}
	defer response.Body.Close()

	// An upstream session serves one turn, so it is closed once the body is
	// drained. A leftover session is invisible to the user, which is why the
	// failure there is logged inside the helper rather than returned.
	defer turn.deleteSession(ctx, accessToken, sessionID)

	forwardUpstreamResponseHeaders(w, response.Header)

	if req.IsStream {
		return deepSeekStreamToClient(w, req, response.Body, envelope.Model)
	}
	return deepSeekCollectToClient(w, req, response.Body, envelope.Model)
}

// deepSeekBaseURL reads the endpoint this connection dials. A connection may
// override it, and the sibling endpoints are derived from whatever it names —
// a request that left for a configured host while the headers and sibling
// calls named the public site would be both detectable and incoherent.
func deepSeekBaseURL(cfg *providers.ProviderConfig) string {
	if cfg == nil || strings.TrimSpace(cfg.BaseURL) == "" {
		return deepSeekWebBase + deepSeekCompletionPath
	}
	return cfg.BaseURL
}

// deepSeekHistoryWindow reads the optional per-connection replay bound. Zero
// means the executor applies its own default.
func deepSeekHistoryWindow(connData map[string]any) int {
	if connData == nil {
		return 0
	}
	switch value := connData["historyWindow"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	}
	return 0
}

// deepSeekStreamToClient relays the upstream event stream as OpenAI chunks.
//
// The stream is only complete when the upstream says so. A body that closes
// without a FINISHED signal was cut off — an expired session, a dropped
// connection — and reporting the partial text as a finished answer is how a
// client comes to treat a truncated reply as the model's real conclusion.
func deepSeekStreamToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model string) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	responseID := deepSeekResponseID()
	created := time.Now().Unix()
	accumulator := newDeepSeekStreamAccumulator(model)

	startTime := req.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}

	var emitErr error
	emit := func(delta map[string]any, finish any) error {
		if emitErr != nil {
			return emitErr
		}
		if req.TTFT != nil && *req.TTFT == 0 {
			*req.TTFT = time.Since(startTime).Milliseconds()
		}
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			emitErr = err
			return err
		}
		if _, err := hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
			emitErr = err
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	// Reasoning and visible text are separate channels on the wire. The role
	// delta rides whichever channel opens first, and each channel is emitted as
	// a delta from what the client has already been sent.
	thinkingSent, contentSent, roleAnnounced := 0, 0, false
	scanErr := proxy.ScanStream(upstream, func(payload []byte) {
		if emitErr != nil {
			return
		}
		var event deepSeekStreamEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return
		}
		accumulator.apply(event)

		if thinking, sent := deepSeekDeltaFrom(accumulator.thinkingText(), thinkingSent); thinking != "" {
			thinkingSent = sent
			delta := map[string]any{"reasoning_content": thinking}
			if !roleAnnounced {
				roleAnnounced = true
				delta["role"] = "assistant"
			}
			_ = emit(delta, nil)
		}
		if content, sent := deepSeekDeltaFrom(accumulator.contentText(), contentSent); content != "" {
			contentSent = sent
			delta := map[string]any{"content": content}
			if !roleAnnounced {
				roleAnnounced = true
				delta["role"] = "assistant"
			}
			_ = emit(delta, nil)
		}
	})
	if scanErr != nil {
		return deepSeekReportStreamAbort(hw, flusher, scanErr)
	}
	if emitErr != nil {
		return emitErr
	}
	if !accumulator.sawFinished {
		return deepSeekReportStreamAbort(hw, flusher,
			fmt.Errorf("deepseek-web stream ended before completion — the upstream session was cut off"))
	}

	if err := emit(map[string]any{}, "stop"); err != nil {
		return err
	}
	_, err := hw.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return err
}

// deepSeekReportStreamAbort reports an aborted upstream stream in-band. HTTP 200
// is already on the wire by this point, so the failure has to travel as a chunk
// carrying an error rather than as a status the client never sees.
func deepSeekReportStreamAbort(hw *proxy.HeartbeatWriter, flusher http.Flusher, cause error) error {
	code, message := proxy.ClassifyStreamAbort(cause)
	_, _ = hw.Write(proxy.BuildStreamErrorBytes(code, message, proxy.SSEFormatOpenAI))
	_, _ = hw.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return cause
}

// deepSeekCollectToClient drives the same stream to completion and returns one
// chat.completion document.
func deepSeekCollectToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model string) error {
	accumulator := newDeepSeekStreamAccumulator(model)
	if err := proxy.ScanStream(upstream, func(payload []byte) {
		var event deepSeekStreamEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return
		}
		accumulator.apply(event)
	}); err != nil {
		return deepSeekFailure(http.StatusBadGateway, "deepseek-web: "+err.Error())
	}
	if !accumulator.sawFinished {
		return deepSeekFailure(http.StatusBadGateway,
			"deepseek-web: stream ended before completion — the upstream session was cut off")
	}

	message := map[string]any{"role": "assistant", "content": accumulator.contentText()}
	if thinking := accumulator.thinkingText(); thinking != "" {
		message["reasoning_content"] = thinking
	}
	body := map[string]any{
		"id":      deepSeekResponseID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("deepseek-web: marshal response: %w", err)
	}

	// The answer goes through the shared response path so a /v1/responses or
	// Claude client receives the shape it asked for, outbound guardrails still
	// apply, and the log buffer records the same text every other provider does.
	return jsonResponse(req.Ctx, w, bytes.NewReader(encoded), req.TranslateResp, req.ResponseBuf)
}

// deepSeekResponseID mints a completion id in the shape OpenAI clients expect.
func deepSeekResponseID() string {
	return "chatcmpl-deepseek-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// deepSeekFailure builds the error the router sees for a failure that is not an
// upstream transport error.
func deepSeekFailure(status int, message string) error {
	return proxy.UpstreamFailure(status, message)
}

// deepSeekAuthFailure maps a failure onto the HTTP status it should surface as,
// and drops the cached access token when it is an auth failure so the next
// attempt re-exchanges the credential instead of replaying a refused one.
func deepSeekAuthFailure(err error, userToken string) error {
	var apiErr *deepSeekAPIError
	if errors.As(err, &apiErr) {
		if apiErr.Code == deepSeekCodeAuthFailed {
			deepSeekTokens.drop(userToken)
			return deepSeekFailure(http.StatusUnauthorized, apiErr.Error())
		}
		if apiErr.Code == deepSeekCodeRateLimited {
			return deepSeekFailure(http.StatusTooManyRequests, apiErr.Error())
		}
		return deepSeekFailure(http.StatusBadGateway, apiErr.Error())
	}
	if isDeepSeekAuthError(err) {
		deepSeekTokens.drop(userToken)
		return deepSeekFailure(http.StatusUnauthorized, "deepseek-web: "+err.Error())
	}
	return deepSeekFailure(http.StatusBadGateway, "deepseek-web: "+err.Error())
}

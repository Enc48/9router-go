fix(codex): strip temperature/top_p before forwarding to the Codex backend

Editors such as Zed send `temperature` on every chat-completions turn, and
`buildResponsesBody` forwarded it as-is. The ChatGPT Codex OAuth backend
answered `400 {"detail":"Unsupported parameter: temperature"}` on the reasoning
models behind Codex, so each turn burned one failed upstream call before the
combo moved on — the 400 is request-scoped, so the connection was never locked
and the failure repeated every time.

`temperature` and `top_p` now join `max_tokens` / `max_completion_tokens` /
`max_output_tokens` in `stripCodexUnsupportedTokenParams`. The strip stays on
the Codex forward path only: `buildResponsesBody` is shared with the other
Responses-API providers (grok-cli, kimi, muse, opencode-zen), whose backends do
accept the field, so moving it there would silently change their requests.

Parity with upstream `open-sse/executors/codex.js`, whose `transformRequest`
deletes `body.temperature` and `body.top_p` alongside the generation limits.

**Verification**: `go test ./internal/proxy/executor/` passes. The fake upstream
in `TestForwardCodex_UpstreamNeverSeesTokenParams` now rejects all five
parameters the way ChatGPT does and the client body carries `temperature` and
`top_p`, so the wiring — not just the helper — is pinned. Manually verified on
Windows with Zed: `gpt-6-astra` went from a 400 on every turn to 200.

**Known gap (not introduced here)**: upstream deletes a longer list
(`frequency_penalty`, `presence_penalty`, `logprobs`, `top_logprobs`, `n`,
`seed`, `user`, `metadata`, `stream_options`, `safety_identifier`,
`previous_response_id`) and then filters the body through a Responses-API
allowlist. This port still deletes only the five fields above, so a client
sending e.g. `frequency_penalty` to Codex can still hit the same 400.
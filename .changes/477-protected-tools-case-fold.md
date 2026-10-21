### Fixed

- The RTK protected-tool list is matched case-insensitively, and now covers the
  spellings this repo actually uses. `protectedTools` was keyed on `Read`,
  `Grep`, `Edit`, `Write`, `patch` and `anchor` and looked up with
  `strings.TrimSpace` alone, so `isProtectedTool("read")` was false — and
  lowercase is the canonical spelling here (`internal/translator/fingerprint.go`
  lists `{"bash", "glob", "grep", "read"}`), plus the one OpenAI-style clients
  send in `tool_calls[].function.name`. A 18 KB `read` result was therefore
  summarised to 72 bytes while the same result under `Read` was preserved. `bash`
  — the main path for `cat`, `sed -n` and `git` output — was not on the list at
  all, so a `bash` result the agent needed verbatim went through the
  content-matched filters and came back as `git: already on branch`. The list now
  names `read`, `write`, `edit`, `multiedit`, `notebookedit`, `patch`, `anchor`,
  `grep`, `glob`, `bash` and `webfetch`, all lowercase, and the lookup folds case.
- A tool error expressed only in the result text is recognised. `isErrorToolResult`
  read `is_error`, `isError` and `status`, none of which exist in the OpenAI Chat
  Completions tool shape — `{"role":"tool","tool_call_id":…,"content":"…"}` — so a
  failing command's trace was compressed on exactly that lane, and the flag does
  not survive `TranslateClaudeToOpenAI` either. The detector now also reads a
  result that opens with an error verdict (`Error:`, `error:`, `failed`,
  `fatal`, `panic`, `exception`, `traceback`), in a string content or in a text
  part, and accepts the string/number spellings of the boolean flags and a
  numeric `status` of 400 or above. The content match is anchored at the start of
  the result, so a file read that merely mentions an error mid-file is not
  treated as a failed tool call.

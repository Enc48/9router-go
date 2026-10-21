package tokensaver

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"regexp"
	"strconv"
	"strings"
)

const (
	MinCompressSize = 500
	SmartTruncHead  = 120
	SmartTruncTail  = 60
	SmartTruncMin   = 250
	GitDiffMaxLines = 100
	GitLogMaxLines  = 200
	GrepPerFileMax  = 10
	TreeMaxLines    = 200
)

// protectedTools names the tool calls whose output must never be summarised.
//
// The keys are lowercase because that is the canonical spelling this repo uses
// for tool names (internal/translator/fingerprint.go:49 lists
// {"bash", "glob", "grep", "read"}), and it is the spelling OpenAI-style
// clients actually send in tool_calls[].function.name. The set is a local
// hardening on top of upstream RTK, which has no protected-tool concept and
// only preserves is_error traces: a file the model asked to read must not come
// back as a summary, because the next turn is issued against it.
//
// Every entry is a tool whose result is prose the model needs verbatim: file
// reads (read, write, edit, patch, multiedit, notebookedit), searches (grep,
// glob) and the generic shell (bash) that carries cat/sed/git output, plus
// webfetch and anchor. Anything not listed here is compressed as before.
var protectedTools = map[string]bool{
	"read":         true,
	"write":        true,
	"edit":         true,
	"multiedit":    true,
	"notebookedit": true,
	"patch":        true,
	"anchor":       true,
	"grep":         true,
	"glob":         true,
	"bash":         true,
	"webfetch":     true,
}

// isProtectedTool folds case before the lookup. The list is keyed lowercase and
// a client may spell the same tool "Read" or "read", so an exact-case map would
// miss the spelling the repo canonicalises to (#477).
func isProtectedTool(name string) bool {
	return protectedTools[strings.ToLower(strings.TrimSpace(name))]
}

// errorResultPrefix recognises an error that is only described in the result
// text. The OpenAI Chat Completions tool shape has no is_error field at all —
// a tool result there is {"role":"tool","tool_call_id":…,"content":"…"} — so a
// detector that only reads typed fields cannot see a failed command (#477).
// The match is anchored at the start of the content, which is where a failing
// tool puts its verdict, and it never inspects the middle of a file read.
var errorResultPrefix = regexp.MustCompile(`(?i)^\s*(?:error|failed|failure|fatal|panic|exception|traceback)\b`)

func isErrorToolResult(m map[string]any) bool {
	if m == nil {
		return false
	}
	if truthyToolFlag(m["is_error"]) || truthyToolFlag(m["isError"]) || truthyToolFlag(m["error"]) {
		return true
	}
	if isErrorStatus(m["status"]) {
		return true
	}
	if content, ok := m["content"].(string); ok && looksLikeToolError(content) {
		return true
	}
	if contentArr, ok := m["content"].([]any); ok {
		for _, part := range contentArr {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := partMap["text"].(string); ok && looksLikeToolError(text) {
				return true
			}
		}
	}
	return false
}

// truthyToolFlag reads a flag that may arrive as a bool, the string "true" or
// the number 1: providers serialise booleans all three ways.
func truthyToolFlag(v any) bool {
	switch flag := v.(type) {
	case bool:
		return flag
	case string:
		return strings.EqualFold(strings.TrimSpace(flag), "true")
	case float64:
		return flag == 1
	case int:
		return flag == 1
	}
	return false
}

// isErrorStatus accepts the word-shaped statuses and any numeric HTTP failure.
func isErrorStatus(v any) bool {
	switch status := v.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "error", "failed", "failure":
			return true
		}
	case float64:
		return status >= 400
	case int:
		return status >= 400
	}
	return false
}

// looksLikeToolError reports whether a tool result opens with an error verdict.
func looksLikeToolError(content string) bool {
	return errorResultPrefix.MatchString(content)
}

func buildToolCallMap(items []map[string]any) map[string]string {
	toolMap := make(map[string]string)
	for _, item := range items {
		// Chat assistant tool_calls
		if tcList, ok := item["tool_calls"].([]any); ok {
			for _, tc := range tcList {
				if tcMap, ok := tc.(map[string]any); ok {
					id, _ := tcMap["id"].(string)
					if fn, ok := tcMap["function"].(map[string]any); ok {
						if name, ok := fn["name"].(string); ok && id != "" {
							toolMap[id] = name
						}
					}
				}
			}
		}
		// Responses function_call
		if t, ok := item["type"].(string); ok && t == "function_call" {
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			if callID != "" && name != "" {
				toolMap[callID] = name
			}
		}
		// Claude tool_use in content array
		if contentArr, ok := item["content"].([]any); ok {
			for _, part := range contentArr {
				if block, ok := part.(map[string]any); ok {
					if bt, ok := block["type"].(string); ok && bt == "tool_use" {
						id, _ := block["id"].(string)
						name, _ := block["name"].(string)
						if id != "" && name != "" {
							toolMap[id] = name
						}
					}
				}
			}
		}
	}
	return toolMap
}

// CompressMessages compresses tool_result content in LLM request bodies in-place.
// Returns modified body and true if any compression was applied.
func CompressMessages(body []byte) ([]byte, bool) {
	return CompressMessagesWithConfig(body, DefaultRTKConfig())
}

// CompressMessagesWithConfig compresses tool_result content in LLM request bodies in-place using RTKConfig.
// Returns modified body and true if any compression was applied.
func CompressMessagesWithConfig(body []byte, cfg RTKConfig) ([]byte, bool) {
	var rawMap map[string]jsontext.Value
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return body, false
	}
	// Unmarshal into map[string]jsontext.Value succeeds for "null" or "[]",
	// leaving rawMap nil. Marshal(nil map) yields "null", which would corrupt
	// the request body downstream, so bail out on non-object payloads.
	if rawMap == nil {
		return body, false
	}

	key := "messages"
	itemsBytes, ok := rawMap["messages"]
	if !ok {
		key = "input"
		itemsBytes, ok = rawMap["input"]
	}
	if !ok || len(itemsBytes) == 0 {
		return body, false
	}

	var items []map[string]any
	if err := json.Unmarshal(itemsBytes, &items); err != nil || len(items) == 0 {
		return body, false
	}

	toolMap := buildToolCallMap(items)
	compressed := false
	for _, msg := range items {
		// OpenAI Responses: function_call_output
		if msg["type"] == "function_call_output" {
			name, _ := msg["name"].(string)
			if name == "" {
				if callID, ok := msg["call_id"].(string); ok {
					name = toolMap[callID]
				}
			}
			if isErrorToolResult(msg) || isProtectedTool(name) {
				continue
			}
			if output, ok := msg["output"].(string); ok && len(output) > MinCompressSize {
				msg["output"] = CompressTextWithConfig(output, cfg)
				compressed = true
			} else if outputArr, ok := msg["output"].([]any); ok {
				for _, part := range outputArr {
					if partMap, ok := part.(map[string]any); ok {
						if t, ok := partMap["type"].(string); ok && (t == "input_text" || t == "text") {
							if text, ok := partMap["text"].(string); ok && len(text) > MinCompressSize {
								partMap["text"] = CompressTextWithConfig(text, cfg)
								compressed = true
							}
						}
					}
				}
			}
			continue
		}

		// OpenAI tool message
		if msg["role"] == "tool" {
			name, _ := msg["name"].(string)
			if name == "" {
				if callID, ok := msg["tool_call_id"].(string); ok {
					name = toolMap[callID]
				}
			}
			if isErrorToolResult(msg) || isProtectedTool(name) {
				continue
			}
			if content, ok := msg["content"].(string); ok && len(content) > MinCompressSize {
				msg["content"] = CompressTextWithConfig(content, cfg)
				compressed = true
			} else if contentArr, ok := msg["content"].([]any); ok {
				for _, part := range contentArr {
					if partMap, ok := part.(map[string]any); ok {
						if t, ok := partMap["type"].(string); ok && t == "text" {
							if text, ok := partMap["text"].(string); ok && len(text) > MinCompressSize {
								partMap["text"] = CompressTextWithConfig(text, cfg)
								compressed = true
							}
						}
					}
				}
			}
			continue
		}

		// Claude tool_result in content array
		contentArr, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range contentArr {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "tool_result" {
				name, _ := block["name"].(string)
				if name == "" {
					if toolUseID, ok := block["tool_use_id"].(string); ok {
						name = toolMap[toolUseID]
					}
				}
				if isErrorToolResult(block) || isProtectedTool(name) {
					continue
				}

				if text, ok := block["text"].(string); ok && len(text) > MinCompressSize {
					block["text"] = CompressTextWithConfig(text, cfg)
					compressed = true
				}
				if content, ok := block["content"].(string); ok && len(content) > MinCompressSize {
					block["content"] = CompressTextWithConfig(content, cfg)
					compressed = true
				} else if innerArr, ok := block["content"].([]any); ok {
					for _, innerPart := range innerArr {
						if innerBlock, ok := innerPart.(map[string]any); ok {
							if innerBlock["type"] == "text" {
								if text, ok := innerBlock["text"].(string); ok && len(text) > MinCompressSize {
									innerBlock["text"] = CompressTextWithConfig(text, cfg)
									compressed = true
								}
							}
						}
					}
				}
			}
			if block["type"] == "text" {
				if text, ok := block["text"].(string); ok && len(text) > MinCompressSize {
					block["text"] = CompressTextWithConfig(text, cfg)
					compressed = true
				}
			}
		}
	}

	if !compressed {
		return body, false
	}

	compressedBytes, err := json.Marshal(items)
	if err != nil {
		return body, false
	}
	rawMap[key] = jsontext.Value(compressedBytes)

	out, err := json.Marshal(rawMap)
	if err != nil {
		return body, false
	}
	return out, true
}

// CompressText applies content-aware compression.
func CompressText(text string) string {
	if len(text) < MinCompressSize {
		return text
	}
	trimmed := strings.TrimSpace(text)

	if isGitDiff(trimmed) {
		return compressGitDiff(trimmed)
	}
	if isGitLog(trimmed) {
		return compressGitLog(trimmed)
	}
	if isGrepOutput(trimmed) {
		return compressGrep(trimmed)
	}
	if isTreeOutput(trimmed) {
		return compressTree(trimmed)
	}
	return smartTruncate(trimmed)
}

func isGitDiff(s string) bool {
	return strings.HasPrefix(s, "diff --git") || strings.HasPrefix(s, "--- a/")
}
func isGitLog(s string) bool {
	lines := strings.SplitN(s, "\n", 2)
	if len(lines) == 0 {
		return false
	}
	matched, _ := regexp.MatchString(`^([a-f0-9]{7,40}\s|commit\s[a-f0-9]{7,})`, lines[0])
	return matched
}
func isGrepOutput(s string) bool {
	lines := strings.SplitN(s, "\n", 4)
	if len(lines) < 2 {
		return false
	}
	count := 0
	for _, l := range lines[:3] {
		if len(strings.SplitN(l, ":", 3)) >= 2 {
			count++
		}
	}
	return count >= 2
}
func isTreeOutput(s string) bool {
	return strings.Contains(s, "├──") || strings.Contains(s, "└──")
}

func compressGitDiff(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= GitDiffMaxLines {
		return s
	}
	return strings.Join(lines[:GitDiffMaxLines], "\n") + "\n... (" + itoa(len(lines)-GitDiffMaxLines) + " more lines)"
}

func compressGitLog(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= GitLogMaxLines {
		return s
	}
	return strings.Join(lines[:GitLogMaxLines], "\n") + "\n... (" + itoa(len(lines)-GitLogMaxLines) + " more commits)"
}

func compressGrep(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= GrepPerFileMax*20 {
		return s
	}
	fileCount := make(map[string]int)
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		file := parts[0]
		if fileCount[file] >= GrepPerFileMax {
			continue
		}
		fileCount[file]++
		result = append(result, line)
	}
	if len(result) < len(lines) {
		result = append(result, "... ("+itoa(len(lines)-len(result))+" matches filtered)")
	}
	return strings.Join(result, "\n")
}

func compressTree(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= TreeMaxLines {
		return s
	}
	return strings.Join(lines[:TreeMaxLines], "\n") + "\n... (" + itoa(len(lines)-TreeMaxLines) + " more entries)"
}

func smartTruncate(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= SmartTruncMin {
		return s
	}
	head := lines[:SmartTruncHead]
	tail := lines[len(lines)-SmartTruncTail:]
	result := append(head, "... ("+itoa(len(lines)-SmartTruncHead-SmartTruncTail)+" lines truncated)")
	return strings.Join(append(result, tail...), "\n")
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

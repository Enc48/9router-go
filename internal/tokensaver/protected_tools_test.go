package tokensaver

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

// TestIsProtectedToolFoldsCase pins the lower-case canonical spelling and every
// capitalisation a client may send. A tool result the model asked to read must
// never be summarised, whichever spelling arrives (#477).
func TestIsProtectedToolFoldsCase(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"read", true},
		{"Read", true},
		{"READ", true},
		{"  read  ", true},
		{"bash", true},
		{"Bash", true},
		{"glob", true},
		{"Glob", true},
		{"grep", true},
		{"Grep", true},
		{"edit", true},
		{"Edit", true},
		{"write", true},
		{"Write", true},
		{"MultiEdit", true},
		{"NotebookEdit", true},
		{"WebFetch", true},
		{"patch", true},
		{"anchor", true},
		{"", false},
		{"   ", false},
		{"cmd", false},
		{"run_tests", false},
		{"terminal", false},
		{"python", false},
	}
	for _, tt := range tests {
		if got := isProtectedTool(tt.name); got != tt.want {
			t.Errorf("isProtectedTool(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestIsErrorToolResultShapes covers every wire shape a tool error arrives in,
// including the OpenAI Chat Completions shape which has no error field at all
// and describes the failure only in the content text (#477).
func TestIsErrorToolResultShapes(t *testing.T) {
	tests := []struct {
		name string
		msg  map[string]any
		want bool
	}{
		{"nil", nil, false},
		{"claude is_error bool", map[string]any{"is_error": true}, true},
		{"claude is_error string", map[string]any{"is_error": "true"}, true},
		{"claude is_error number", map[string]any{"is_error": float64(1)}, true},
		{"is_error false", map[string]any{"is_error": false}, false},
		{"isError camel", map[string]any{"isError": true}, true},
		{"error flag", map[string]any{"error": true}, true},
		{"status word", map[string]any{"status": "error"}, true},
		{"status uppercase", map[string]any{"status": "ERROR"}, true},
		{"status failed", map[string]any{"status": "failed"}, true},
		{"status numeric 500", map[string]any{"status": float64(500)}, true},
		{"status numeric 200", map[string]any{"status": float64(200)}, false},
		{"status success", map[string]any{"status": "success"}, false},
		{"openai content error", map[string]any{"role": "tool", "content": "Error: file not found\nline of failing command output"}, true},
		{"openai content lowercase", map[string]any{"role": "tool", "content": "error: no such file or directory"}, true},
		{"openai content traceback", map[string]any{"role": "tool", "content": "Traceback (most recent call last):\n  File \"x.py\""}, true},
		{"openai content array", map[string]any{"role": "tool", "content": []any{
			map[string]any{"type": "text", "text": "Error: build failed"},
		}}, true},
		{"openai content ok", map[string]any{"role": "tool", "content": "main.go:12: func main() {}"}, false},
		{"content mentions error mid-line", map[string]any{"role": "tool", "content": "main.go\nlog.Error(\"boom\")"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isErrorToolResult(tt.msg); got != tt.want {
				t.Errorf("isErrorToolResult(%v) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

// TestCompressPreservesLowercaseToolsAndErrorText runs the shapes end to end
// through CompressMessages: a lower-case `read`/`bash` result and an OpenAI tool
// error must come back byte-identical, while an unprotected tool still compresses.
func TestCompressPreservesLowercaseToolsAndErrorText(t *testing.T) {
	fileText := strings.Repeat("file line content here\n", 300)
	errorText := "Error: build failed at main.go:12\n" + strings.Repeat("trace line\n", 300)

	tests := []struct {
		name        string
		toolName    string
		content     string
		wantCompres bool
	}{
		{"lowercase read", "read", fileText, false},
		{"lowercase bash", "bash", fileText, false},
		{"capitalised Bash", "Bash", fileText, false},
		{"lowercase glob", "glob", fileText, false},
		{"opaque tool still compresses", "cmd", fileText, true},
		{"opaque tool error text preserved", "run_tests", errorText, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{
				"messages": []any{
					map[string]any{"role": "assistant", "content": "x", "tool_calls": []any{
						map[string]any{"id": "tc1", "type": "function", "function": map[string]any{"name": tt.toolName}},
					}},
					map[string]any{"role": "tool", "tool_call_id": "tc1", "content": tt.content},
				},
			}
			in, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			out, compressed := CompressMessages(in)
			if compressed != tt.wantCompres {
				t.Fatalf("compressed = %v, want %v", compressed, tt.wantCompres)
			}
			if !tt.wantCompres && string(out) != string(in) {
				t.Errorf("body changed although the result is protected (%d -> %d bytes)", len(in), len(out))
			}
		})
	}
}

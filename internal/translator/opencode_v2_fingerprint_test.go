package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestConcealOpenCodeV2FingerprintToolsInjects(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		flat       bool
	}{
		{"chat", "{\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}", false},
		{"responses", "{\"input\":\"hi\"}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := ConcealOpenCodeV2FingerprintTools([]byte(tc.body), tc.flat)
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			tools, _ := got["tools"].([]any)
			if len(tools) != 2 {
				t.Fatalf("got %d tools, want read and shell", len(tools))
			}
			if tc.flat {
				if got["tool_choice"] != "auto" {
					t.Errorf("tool_choice=%v, want auto", got["tool_choice"])
				}
				if !strings.Contains(got["instructions"].(string), "placeholders") {
					t.Error("missing placeholder instruction")
				}
			} else {
				messages := got["messages"].([]any)
				if messages[0].(map[string]any)["role"] != "system" {
					t.Error("missing placeholder system note")
				}
			}
		})
	}
}

func TestConcealOpenCodeV2AliasesCallerReadAndHistory(t *testing.T) {
	body := []byte("{\"messages\":[{\"role\":\"assistant\",\"tool_calls\":[{\"function\":{\"name\":\"read\"}}]}],\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"read\",\"description\":\"caller read\",\"parameters\":{\"type\":\"object\",\"properties\":{}}}}],\"tool_choice\":{\"type\":\"function\",\"function\":{\"name\":\"read\"}}}")
	out, names := ConcealOpenCodeV2FingerprintTools(body, false)
	if names["read_client"] != "read" {
		t.Fatalf("alias map = %v", names)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	call := messages[len(messages)-1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if call["name"] != "read_client" {
		t.Errorf("history tool name=%v", call["name"])
	}
	choice := got["tool_choice"].(map[string]any)["function"].(map[string]any)
	if choice["name"] != "read_client" {
		t.Errorf("tool choice name=%v", choice["name"])
	}
}

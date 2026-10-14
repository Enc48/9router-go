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
			out, _ := ConcealOpenCodeV2FingerprintTools([]byte(tc.body), tc.flat, false)
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
	out, names := ConcealOpenCodeV2FingerprintTools(body, false, false)
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

// A harness body carries shapes a clean direct request never has. Each case
// pins one such shape to the form the /inference/v1 gate accepts, so whatever
// the caller sends no longer decides between 200 and 403.
func TestConcealOpenCodeV2CanonicalizesHarnessShapes(t *testing.T) {
	t.Run("responses tools are rebuilt into the flat shape and foreign keys dropped", func(t *testing.T) {
		body := []byte(`{"input":"hi","tools":[
			{"type":"function","name":"Read","description":"caller read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"cache_control":{"type":"ephemeral"},"strict":true},
			{"type":"function","name":"","parameters":{"type":"object"}},
			"not-an-object"
		]}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, true, false)
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		tools, _ := got["tools"].([]any)
		// Only the caller's Read survives plus the two injected fingerprints.
		if len(tools) != 3 {
			t.Fatalf("got %d tools, want 3: %s", len(tools), out)
		}
		for _, tool := range tools {
			m := tool.(map[string]any)
			for _, foreign := range []string{"cache_control", "function", "strict"} {
				if _, has := m[foreign]; has {
					t.Errorf("tool %v still carries foreign key %q", m["name"], foreign)
				}
			}
			if _, has := m["name"]; !has {
				t.Errorf("tool missing canonical name: %v", m)
			}
			params, _ := m["parameters"].(map[string]any)
			if params == nil || params["type"] != "object" {
				t.Errorf("tool %v missing default schema: %v", m["name"], m["parameters"])
			}
		}
	})

	t.Run("chat tools without a schema gain the default object schema", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"Task","description":"run a task"}}]}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, false, false)
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		tools, _ := got["tools"].([]any)
		for _, tool := range tools {
			m := tool.(map[string]any)
			fn := m["function"].(map[string]any)
			params, _ := fn["parameters"].(map[string]any)
			if params == nil || params["type"] != "object" {
				t.Errorf("tool %v left without a schema: %v", fn["name"], fn["parameters"])
			}
		}
	})

	t.Run("tool_choice none and auto-object collapse to auto on both lanes", func(t *testing.T) {
		for _, tc := range []struct {
			name, body string
			flat       bool
		}{
			{"chat none string", `{"messages":[],"tool_choice":"none"}`, false},
			{"chat none object", `{"messages":[],"tool_choice":{"type":"none"}}`, false},
			{"responses none string", `{"input":[],"tool_choice":"none"}`, true},
			{"responses auto object", `{"input":[],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				out, _ := ConcealOpenCodeV2FingerprintTools([]byte(tc.body), tc.flat, false)
				var got map[string]any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatal(err)
				}
				if got["tool_choice"] != "auto" {
					t.Errorf("tool_choice=%v, want auto (gate rejects none and object forms): %s", got["tool_choice"], out)
				}
			})
		}
	})

	t.Run("auto-only models get any forced choice rewritten to auto", func(t *testing.T) {
		for _, tc := range []struct {
			name, body string
			flat       bool
		}{
			{"chat forced function", `{"messages":[],"tool_choice":{"type":"function","function":{"name":"Task"}}}`, false},
			{"responses forced tool", `{"input":[],"tool_choice":{"type":"tool","name":"Task"}}`, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				out, _ := ConcealOpenCodeV2FingerprintTools([]byte(tc.body), tc.flat, true)
				var got map[string]any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatal(err)
				}
				if got["tool_choice"] != "auto" {
					t.Errorf("tool_choice=%v, want auto for an auto-only model: %s", got["tool_choice"], out)
				}
			})
		}
	})

	t.Run("responses drops a forced choice naming no declared tool", func(t *testing.T) {
		body := []byte(`{"input":"hi","tools":[{"type":"function","name":"Task","parameters":{"type":"object","properties":{}}}],"tool_choice":{"type":"function","name":"DoesNotExist"}}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, true, false)
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if _, has := got["tool_choice"]; has {
			t.Errorf("tool_choice=%v, want dropped (names no declared tool): %s", got["tool_choice"], out)
		}
	})

	t.Run("responses keeps a forced choice naming a declared tool", func(t *testing.T) {
		body := []byte(`{"input":"hi","tools":[{"type":"function","name":"Task","parameters":{"type":"object","properties":{}}}],"tool_choice":{"type":"function","name":"Task"}}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, true, false)
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		choice, ok := got["tool_choice"].(map[string]any)
		if !ok {
			t.Fatalf("tool_choice=%v, want the forced choice preserved: %s", got["tool_choice"], out)
		}
		if choice["name"] != "Task" {
			t.Errorf("forced choice name=%v, want Task", choice["name"])
		}
	})

	t.Run("nameless prior function_call items are dropped from responses input", func(t *testing.T) {
		body := []byte(`{"input":[
			{"type":"function_call","call_id":"call_1","arguments":"{}"},
			{"type":"function_call","call_id":"call_2","name":"Task","arguments":"{}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
		]}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, true, false)
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		input, _ := got["input"].([]any)
		for _, item := range input {
			m := item.(map[string]any)
			if m["type"] == "function_call" {
				if name, _ := m["name"].(string); strings.TrimSpace(name) == "" {
					t.Errorf("nameless function_call survived: %s", out)
				}
			}
		}
	})

	t.Run("an exact caller read definition is kept without aliasing", func(t *testing.T) {
		tool, err := openCodeV2Tool("read", false)
		if err != nil {
			t.Fatal(err)
		}
		toolJSON, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[` + string(toolJSON) + `]}`)
		out, names := ConcealOpenCodeV2FingerprintTools(body, false, false)
		if len(names) != 0 {
			t.Errorf("alias map = %v, want empty (definition already exact)", names)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		tools, _ := got["tools"].([]any)
		if len(tools) != 2 {
			t.Fatalf("got %d tools, want the exact caller read plus the injected shell: %s", len(tools), out)
		}
		messages, _ := got["messages"].([]any)
		for _, msg := range messages {
			m := msg.(map[string]any)
			if m["role"] == "system" && strings.Contains(m["content"].(string), "Do not call the read") {
				t.Errorf("read was marked a placeholder even though the caller declared it exactly: %s", out)
			}
		}
	})
}

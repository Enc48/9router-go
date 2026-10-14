package translator

import (
	json "encoding/json/v2"
	"strconv"
	"strings"
)

const openCodeReadDescription = "Read the contents of a file or directory. Supports text files, images, and PDFs. Images and PDFs are presented directly to the model. Each text line is prefixed by its 1-based line number as <line>: <content>. The prefix is for reference and is not part of the file content. Directory entries are returned one per line. Use offset and limit to read large files or directories in sections. Prefer one larger read over many small slices, and use grep to find specific content in large files."
const openCodeShellDescription = "Execute a shell command and return its output. Commands run on Linux using bash. Quote file paths containing spaces or special characters. Prefer dedicated tools over shell commands when possible. When output is large, the full result is saved to a file and a truncated preview is returned. Rely on automatic truncation unless filtering the output is more useful. Commands accept an optional timeout, background commands have no timeout by default. Background commands return immediately, and you will be notified when they complete. DO NOT poll for completion."

// ConcealOpenCodeV2FingerprintTools ports the OpenCode v2 read/shell
// fingerprint. flat is true for Responses API tools and false for chat tools.
func ConcealOpenCodeV2FingerprintTools(body []byte, flat bool) ([]byte, map[string]string) {
	var request map[string]any
	if len(body) == 0 || json.Unmarshal(body, &request) != nil {
		return body, map[string]string{}
	}
	tools, _ := request["tools"].([]any)
	nameMap := make(map[string]string)
	taken, seen := make(map[string]bool), make(map[string]bool)
	for _, tool := range tools {
		if name := toolNameOf(tool); name != "" {
			taken[name] = true
		}
	}
	kept := make([]any, 0, len(tools)+2)
	for _, tool := range tools {
		name := toolNameOf(tool)
		if name != "read" && name != "shell" {
			kept = append(kept, tool)
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		toolMap, _ := tool.(map[string]any)
		definition := toolMap
		if fn, ok := toolMap["function"].(map[string]any); ok {
			definition = fn
		}
		wantDescription := openCodeReadDescription
		if name == "shell" {
			wantDescription = openCodeShellDescription
		}
		params, _ := json.Marshal(definition["parameters"], json.Deterministic(true))
		if definition["description"] == wantDescription && string(params) == openCodeV2Parameters(name) {
			kept = append(kept, tool)
			continue
		}
		alias := name + "_client"
		for i := 2; taken[alias]; i++ {
			alias = name + "_client" + strconv.Itoa(i)
		}
		taken[alias], nameMap[alias] = true, name
		setToolName(tool, alias)
		kept = append(kept, tool)
	}
	appended := make([]string, 0, 2)
	for _, name := range []string{"read", "shell"} {
		if seen[name] {
			continue
		}
		tool, err := openCodeV2Tool(name, flat)
		if err != nil {
			return body, map[string]string{}
		}
		kept = append(kept, tool)
		appended = append(appended, name)
	}
	request["tools"] = kept
	if len(nameMap) > 0 {
		aliasOpenCodeV2History(request, nameMap)
		aliasOpenCodeV2Choice(request, nameMap)
	}
	if choice, ok := request["tool_choice"].(map[string]any); ok && choice["type"] == "none" {
		request["tool_choice"] = "auto"
	}
	if flat && request["tool_choice"] == nil {
		request["tool_choice"] = "auto"
	}
	if len(appended) > 0 {
		note := "Do not call the " + strings.Join(appended, " or ") + " tools; they are placeholders that do nothing. Use the other tools you have been given instead."
		if flat {
			if s, ok := request["instructions"].(string); ok && strings.TrimSpace(s) != "" {
				request["instructions"] = s + "\n\n" + note
			} else {
				request["instructions"] = note
			}
		}
		if !flat {
			addOpenCodeChatNote(request, note)
		}
	}
	out, err := json.Marshal(request, json.Deterministic(true))
	if err != nil {
		return body, map[string]string{}
	}
	return out, nameMap
}

func openCodeV2Parameters(name string) string {
	if name == "read" {
		return "{\"additionalProperties\":false,\"properties\":{\"limit\":{\"description\":\"The maximum number of lines or directory entries to read (defaults to and capped at 2000)\",\"minimum\":0,\"type\":\"integer\"},\"offset\":{\"description\":\"The line or directory entry to start reading from (1-based)\",\"minimum\":0,\"type\":\"integer\"},\"path\":{\"description\":\"File or directory to read\",\"type\":\"string\"}},\"required\":[\"path\"],\"type\":\"object\"}"
	}
	return "{\"additionalProperties\":false,\"properties\":{\"background\":{\"description\":\"Run the command in the background and return immediately (useful for long-running commands). You do not need to use '&' at the end of the command when using this parameter. You will be notified when they complete. DO NOT poll for completion.\",\"type\":\"boolean\"},\"command\":{\"description\":\"Shell command string to execute\",\"type\":\"string\"},\"timeout\":{\"description\":\"Timeout in milliseconds. Set to 0 to disable the timeout. Defaults to 120000 for foreground commands. Background commands have no timeout by default.\",\"minimum\":0,\"type\":\"integer\"},\"workdir\":{\"description\":\"Working directory to execute the command in. Defaults to the current working directory. When possible, avoid changing directories in the command and set the working directory here instead.\",\"type\":\"string\"}},\"required\":[\"command\"],\"type\":\"object\"}"
}

func openCodeV2Tool(name string, flat bool) (map[string]any, error) {
	parameters := openCodeV2Parameters(name)
	var schema map[string]any
	if err := json.Unmarshal([]byte(parameters), &schema); err != nil {
		return nil, err
	}
	description := openCodeReadDescription
	if name == "shell" {
		description = openCodeShellDescription
	}
	if flat {
		return map[string]any{"type": "function", "name": name, "description": description, "parameters": schema, "strict": false}, nil
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": schema}}, nil
}

func aliasOpenCodeV2History(request map[string]any, sentToOriginal map[string]string) {
	aliases := make(map[string]string, len(sentToOriginal))
	for alias, original := range sentToOriginal {
		aliases[original] = alias
	}
	if input, ok := request["input"].([]any); ok {
		for _, item := range input {
			if m, ok := item.(map[string]any); ok && m["type"] == "function_call" {
				if n, ok := m["name"].(string); ok && aliases[n] != "" {
					m["name"] = aliases[n]
				}
			}
		}
	}
	if messages, ok := request["messages"].([]any); ok {
		for _, msg := range messages {
			m, ok := msg.(map[string]any)
			if !ok {
				continue
			}
			calls, _ := m["tool_calls"].([]any)
			for _, call := range calls {
				c, ok := call.(map[string]any)
				if !ok {
					continue
				}
				fn, _ := c["function"].(map[string]any)
				if n, ok := fn["name"].(string); ok && aliases[n] != "" {
					fn["name"] = aliases[n]
				}
			}
		}
	}
}

func aliasOpenCodeV2Choice(request map[string]any, sentToOriginal map[string]string) {
	aliases := make(map[string]string, len(sentToOriginal))
	for alias, original := range sentToOriginal {
		aliases[original] = alias
	}
	choice, ok := request["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	if name, ok := choice["name"].(string); ok && aliases[name] != "" {
		choice["name"] = aliases[name]
	}
	if fn, ok := choice["function"].(map[string]any); ok {
		if name, ok := fn["name"].(string); ok && aliases[name] != "" {
			fn["name"] = aliases[name]
		}
	}
}

func addOpenCodeChatNote(request map[string]any, note string) {
	messages, _ := request["messages"].([]any)
	if len(messages) > 0 {
		if first, ok := messages[0].(map[string]any); ok && first["role"] == "system" {
			if content, ok := first["content"].(string); ok {
				first["content"] = content + "\n\n" + note
				return
			}
		}
	}
	message := map[string]any{"role": "system", "content": note}
	if system, ok := request["system"].(string); ok && strings.TrimSpace(system) != "" {
		message["content"] = system + "\n\n" + note
	}
	request["messages"] = append([]any{message}, messages...)
}

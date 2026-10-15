package executor

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
)

// deepSeekMessage is the inbound OpenAI message shape. Content and tool_calls
// stay raw JSON so a field this prompt builder does not read costs no
// intermediate allocation on a long agent transcript.
type deepSeekMessage struct {
	Role       string         `json:"role"`
	Content    jsontext.Value `json:"content"`
	ToolCalls  jsontext.Value `json:"tool_calls"`
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
}

// deepSeekTurnLine is one rendered line of the replayed conversation.
type deepSeekTurnLine struct {
	role string
	text string
}

// deepSeekDefaultAutoHistoryWindow bounds the auto-replayed trajectory when a
// connection configures no window of its own, matching upstream's
// DEFAULT_AUTO_HISTORY_WINDOW.
//
// This is the difference between a chat provider and an agentic one for this
// upstream. /api/v0/chat/completion accepts a single `prompt` string and no
// message array, so a whole conversation has to be folded into one. Sending
// only the last user message makes an agent client silently lose the original
// task after a couple of tool-result turns, because only the newest line ever
// reaches the model.
const deepSeekDefaultAutoHistoryWindow = 20

// deepSeekMessageText extracts the plain text of one message. This format has
// no image or attachment slot, so only text parts survive; non-text parts are
// dropped rather than serialized as JSON the model would have to be told to
// ignore.
func deepSeekMessageText(content jsontext.Value) string {
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return asString
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			b.WriteString(part.Text)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// deepSeekMessagesToPrompt renders an OpenAI conversation as the single prompt
// string the web API accepts.
//
// System prompts lead, the conversation is replayed as a role-tagged transcript
// when it is genuinely multi-turn, and a single-turn request collapses to just
// its user message. Tool results have no native slot in this format, so they are
// folded into the transcript as labelled text: without that the model never sees
// what its tool returned, and either re-calls it forever or claims it has no
// information.
//
// historyWindow bounds the replayed turns. Zero means auto: a genuinely
// multi-turn conversation replays up to deepSeekDefaultAutoHistoryWindow turns,
// while a single-turn one sends only its user message, so an ordinary chat turn
// is not inflated by a transcript it does not need.
func deepSeekMessagesToPrompt(messages []deepSeekMessage, historyWindow int) string {
	if len(messages) == 0 {
		return ""
	}

	var systemParts []string
	var conversation []deepSeekTurnLine
	toolNames := make(map[string]string)
	lastUser := ""

	for _, message := range messages {
		text := strings.TrimSpace(deepSeekMessageText(message.Content))
		switch message.Role {
		case "system":
			if text != "" {
				systemParts = append(systemParts, text)
			}
		case "user", "assistant":
			if text != "" {
				conversation = append(conversation, deepSeekTurnLine{role: message.Role, text: text})
			}
			if message.Role == "user" {
				lastUser = text
			}
			deepSeekCollectToolNames(message.ToolCalls, toolNames)
		case "tool":
			if text == "" {
				continue
			}
			name := message.Name
			if resolved, ok := toolNames[message.ToolCallID]; ok && resolved != "" {
				name = resolved
			}
			if name == "" {
				name = "tool"
			}
			conversation = append(conversation, deepSeekTurnLine{role: "tool", text: "(" + name + ") " + text})
		}
	}

	var parts []string
	if len(systemParts) > 0 {
		parts = append(parts, strings.Join(systemParts, "\n\n"))
	}

	window := historyWindow
	if window <= 0 && len(conversation) > 1 {
		window = deepSeekDefaultAutoHistoryWindow
	}

	if window > 0 && len(conversation) > 1 {
		recent := conversation
		if len(recent) > window {
			recent = recent[len(recent)-window:]
		}
		parts = append(parts, deepSeekRenderTranscript(recent))
	} else if lastUser != "" {
		parts = append(parts, lastUser)
	}

	return strings.Join(parts, "\n\n")
}

// deepSeekRenderTranscript renders replayed turns with the role labels the web
// model was trained on.
func deepSeekRenderTranscript(turns []deepSeekTurnLine) string {
	lines := make([]string, 0, len(turns))
	for _, turn := range turns {
		switch turn.role {
		case "assistant":
			lines = append(lines, "Assistant: "+turn.text)
		case "tool":
			lines = append(lines, "Tool result "+turn.text)
		default:
			lines = append(lines, "User: "+turn.text)
		}
	}
	return strings.Join(lines, "\n\n")
}

// deepSeekToolCall is one entry of an assistant turn's tool_calls array.
type deepSeekToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

// deepSeekCollectToolNames maps each tool_call id to the function it invoked, so
// a later `tool` turn can be labelled with the name the model itself chose
// rather than a generic one.
func deepSeekCollectToolNames(toolCalls jsontext.Value, into map[string]string) {
	if len(toolCalls) == 0 {
		return
	}
	var calls []deepSeekToolCall
	if err := json.Unmarshal(toolCalls, &calls); err != nil {
		return
	}
	for _, call := range calls {
		if call.ID != "" && call.Function.Name != "" {
			into[call.ID] = call.Function.Name
		}
	}
}

// deepSeekRequestEnvelope is the subset of the inbound request body that changes
// what is sent upstream. Everything else is dropped, because this upstream
// accepts only the fields in deepSeekCompletionRequest.
type deepSeekRequestEnvelope struct {
	Model           string            `json:"model"`
	Messages        []deepSeekMessage `json:"messages"`
	ThinkingEnabled bool              `json:"thinking_enabled"`
	Thinking        bool              `json:"thinking"`
	ReasoningEffort string            `json:"reasoning_effort"`
	SearchEnabled   bool              `json:"search_enabled"`
	Search          bool              `json:"search"`
	WebSearch       bool              `json:"web_search"`
}

// deepSeekModelOptions is how a requested model maps onto this upstream's two
// switches. It has no per-model catalogue of its own: the web UI exposes one
// model with two behaviours, and the requested id is where a client's choice
// survives.
type deepSeekModelOptions struct {
	modelType string
	thinking  bool
	search    bool
}

// resolveDeepSeekModelOptions derives the upstream switches from the requested
// model and the request body, matching upstream's resolveModelOptions: "pro" and
// "expert" pick the reasoning model, and think/reason/r1 or an explicit
// reasoning_effort turn reasoning on; "search" or an explicit search flag turns
// web search on.
func resolveDeepSeekModelOptions(model string, body deepSeekRequestEnvelope) deepSeekModelOptions {
	name := strings.ToLower(model)
	options := deepSeekModelOptions{
		modelType: "default",
		thinking: strings.Contains(name, "r1") ||
			strings.Contains(name, "think") ||
			strings.Contains(name, "reason") ||
			body.ThinkingEnabled || body.Thinking || body.ReasoningEffort != "",
		search: strings.Contains(name, "search") ||
			body.SearchEnabled || body.Search || body.WebSearch,
	}
	if strings.Contains(name, "pro") || strings.Contains(name, "expert") {
		options.modelType = "expert"
	}
	return options
}

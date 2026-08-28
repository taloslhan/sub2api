package sessionarchive

import (
	"encoding/json"
	"fmt"
	"strings"
)

type sftNormalizer struct {
	messages      []any
	seenCallIDs   map[string]struct{}
	seenResultIDs map[string]struct{}
}

// normalizeSFTSample 只接受能无歧义还原为 OpenAI Chat messages/tools 的已知协议结构。
// 流式事件、任意 JSON 与结构不完整的响应均 fail-closed，不猜测训练语义。
func normalizeSFTSample(requestBody, responseBody []byte) (sftSample, string) {
	var request, response map[string]any
	if json.Unmarshal(requestBody, &request) != nil || request == nil {
		return sftSample{}, "request_invalid_json"
	}
	if json.Unmarshal(responseBody, &response) != nil || response == nil {
		return sftSample{}, "response_invalid_json"
	}
	normalizer := &sftNormalizer{
		messages:      make([]any, 0, 8),
		seenCallIDs:   make(map[string]struct{}),
		seenResultIDs: make(map[string]struct{}),
	}
	var tools []any
	var requestOK, responseOK bool
	switch {
	case request["contents"] != nil:
		tools, requestOK, responseOK = normalizer.normalizeGemini(request, response)
	case request["input"] != nil:
		tools, requestOK, responseOK = normalizer.normalizeResponses(request, response)
	case request["messages"] != nil:
		if _, chatResponse := response["choices"]; chatResponse {
			tools, requestOK, responseOK = normalizer.normalizeOpenAIChat(request, response)
		} else {
			tools, requestOK, responseOK = normalizer.normalizeAnthropic(request, response)
		}
	default:
		return sftSample{}, "request_unsupported_schema"
	}
	if !requestOK {
		return sftSample{}, "request_unsupported_schema"
	}
	if !responseOK || len(normalizer.messages) < 2 {
		return sftSample{}, "response_unsupported_schema"
	}
	return sftSample{Messages: normalizer.messages, Tools: tools}, ""
}

func (n *sftNormalizer) normalizeOpenAIChat(request, response map[string]any) ([]any, bool, bool) {
	tools, ok := normalizeFunctionTools(request["tools"], "openai")
	if !ok {
		return nil, false, false
	}
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, false, false
	}
	for _, value := range messages {
		if !n.appendOpenAIMessage(value, "") {
			return nil, false, false
		}
	}
	choices, ok := response["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, true, false
	}
	choice, ok := choices[0].(map[string]any)
	beforeResponse := len(n.messages)
	if !ok || !n.appendOpenAIMessage(choice["message"], "assistant") || len(n.messages) == beforeResponse {
		return nil, true, false
	}
	return tools, true, true
}

func (n *sftNormalizer) normalizeAnthropic(request, response map[string]any) ([]any, bool, bool) {
	tools, ok := normalizeFunctionTools(request["tools"], "anthropic")
	if !ok {
		return nil, false, false
	}
	if system, exists := request["system"]; exists {
		content, valid := canonicalTextContent(system)
		if !valid || content == "" {
			return nil, false, false
		}
		n.messages = append(n.messages, map[string]any{"role": "system", "content": content})
	}
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, false, false
	}
	for _, value := range messages {
		if !n.appendAnthropicMessage(value, "") {
			return nil, false, false
		}
	}
	beforeResponse := len(n.messages)
	if !n.appendAnthropicMessage(response, "assistant") || len(n.messages) == beforeResponse {
		return nil, true, false
	}
	return tools, true, true
}

func (n *sftNormalizer) normalizeResponses(request, response map[string]any) ([]any, bool, bool) {
	tools, ok := normalizeFunctionTools(request["tools"], "responses")
	if !ok {
		return nil, false, false
	}
	switch input := request["input"].(type) {
	case string:
		if strings.TrimSpace(input) == "" {
			return nil, false, false
		}
		n.messages = append(n.messages, map[string]any{"role": "user", "content": input})
	case []any:
		if len(input) == 0 {
			return nil, false, false
		}
		for _, item := range input {
			if !n.appendResponseItem(item, "user") {
				return nil, false, false
			}
		}
	default:
		return nil, false, false
	}
	if outputs, ok := response["output"].([]any); ok && len(outputs) > 0 {
		beforeResponse := len(n.messages)
		for _, output := range outputs {
			if !n.appendResponseItem(output, "assistant") {
				return nil, true, false
			}
		}
		return tools, true, len(n.messages) > beforeResponse
	}
	if outputText, ok := response["output_text"].(string); ok && strings.TrimSpace(outputText) != "" {
		n.messages = append(n.messages, map[string]any{"role": "assistant", "content": outputText})
		return tools, true, true
	}
	return nil, true, false
}

func (n *sftNormalizer) normalizeGemini(request, response map[string]any) ([]any, bool, bool) {
	tools, ok := normalizeGeminiTools(request["tools"])
	if !ok {
		return nil, false, false
	}
	for _, key := range []string{"systemInstruction", "system_instruction"} {
		if system, exists := request[key]; exists {
			value, valid := system.(map[string]any)
			if !valid {
				return nil, false, false
			}
			content, valid := geminiTextContent(value["parts"])
			if !valid || content == "" {
				return nil, false, false
			}
			n.messages = append(n.messages, map[string]any{"role": "system", "content": content})
			break
		}
	}
	contents, ok := request["contents"].([]any)
	if !ok || len(contents) == 0 {
		return nil, false, false
	}
	pendingByName := make(map[string][]string)
	syntheticSequence := 0
	for _, content := range contents {
		if !n.appendGeminiContent(content, "user", pendingByName, &syntheticSequence) {
			return nil, false, false
		}
	}
	candidates, ok := response["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return nil, true, false
	}
	candidate, ok := candidates[0].(map[string]any)
	beforeResponse := len(n.messages)
	if !ok || !n.appendGeminiContent(candidate["content"], "assistant", pendingByName, &syntheticSequence) || len(n.messages) == beforeResponse {
		return nil, true, false
	}
	return tools, true, true
}

func (n *sftNormalizer) appendOpenAIMessage(value any, defaultRole string) bool {
	message, ok := value.(map[string]any)
	if !ok {
		return false
	}
	role, _ := message["role"].(string)
	if role == "" {
		role = defaultRole
	}
	if role == "developer" {
		role = "system"
	}
	if role == "tool" {
		callID, _ := message["tool_call_id"].(string)
		content, valid := canonicalToolContent(message["content"])
		return valid && n.appendToolResult(callID, content)
	}
	if role != "system" && role != "user" && role != "assistant" {
		return false
	}
	content, valid := canonicalTextContent(message["content"])
	if !valid {
		return false
	}
	toolCalls := make([]any, 0, 2)
	hadToolCalls := false
	if calls, exists := message["tool_calls"]; exists {
		values, valid := calls.([]any)
		if !valid {
			return false
		}
		hadToolCalls = len(values) > 0
		for _, value := range values {
			call, keep, valid := n.canonicalOpenAIToolCall(value)
			if !valid {
				return false
			}
			if keep {
				toolCalls = append(toolCalls, call)
			}
		}
	}
	if content == "" && len(toolCalls) == 0 {
		return hadToolCalls
	}
	canonical := map[string]any{"role": role}
	if content != "" {
		canonical["content"] = content
	}
	if len(toolCalls) > 0 {
		canonical["tool_calls"] = toolCalls
	}
	n.messages = append(n.messages, canonical)
	return true
}

func (n *sftNormalizer) appendAnthropicMessage(value any, defaultRole string) bool {
	message, ok := value.(map[string]any)
	if !ok {
		return false
	}
	role, _ := message["role"].(string)
	if role == "" {
		role = defaultRole
	}
	if role != "user" && role != "assistant" {
		return false
	}
	if content, ok := message["content"].(string); ok {
		if content == "" {
			return false
		}
		n.messages = append(n.messages, map[string]any{"role": role, "content": content})
		return true
	}
	blocks, ok := message["content"].([]any)
	if !ok || len(blocks) == 0 {
		return false
	}
	textParts, toolCalls, toolResults := make([]string, 0, 2), make([]any, 0, 2), make([]map[string]any, 0, 2)
	recognized := false
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			return false
		}
		typeName, _ := block["type"].(string)
		switch typeName {
		case "text":
			recognized = true
			text, ok := block["text"].(string)
			if !ok {
				return false
			}
			textParts = append(textParts, text)
		case "tool_use":
			recognized = true
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			call, keep, valid := n.canonicalToolCall(id, name, block["input"])
			if !valid || role != "assistant" {
				return false
			}
			if keep {
				toolCalls = append(toolCalls, call)
			}
		case "tool_result":
			recognized = true
			id, _ := block["tool_use_id"].(string)
			content, valid := canonicalToolContent(block["content"])
			if !valid || role != "user" || id == "" {
				return false
			}
			if _, duplicate := n.seenResultIDs[id]; !duplicate {
				n.seenResultIDs[id] = struct{}{}
				toolResults = append(toolResults, map[string]any{"role": "tool", "tool_call_id": id, "content": content})
			}
		default:
			return false
		}
	}
	if len(textParts) > 0 || len(toolCalls) > 0 {
		canonical := map[string]any{"role": role}
		if text := strings.Join(textParts, "\n"); text != "" {
			canonical["content"] = text
		}
		if len(toolCalls) > 0 {
			canonical["tool_calls"] = toolCalls
		}
		n.messages = append(n.messages, canonical)
	}
	for _, result := range toolResults {
		n.messages = append(n.messages, result)
	}
	return recognized
}

func (n *sftNormalizer) appendResponseItem(value any, defaultRole string) bool {
	item, ok := value.(map[string]any)
	if !ok {
		return false
	}
	typeName, _ := item["type"].(string)
	switch typeName {
	case "function_call":
		id, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		call, keep, valid := n.canonicalToolCall(id, name, item["arguments"])
		if !valid {
			return false
		}
		if keep {
			n.messages = append(n.messages, map[string]any{"role": "assistant", "tool_calls": []any{call}})
		}
		return true
	case "function_call_output":
		id, _ := item["call_id"].(string)
		content, valid := canonicalToolContent(item["output"])
		return valid && n.appendToolResult(id, content)
	case "message", "":
		return n.appendOpenAIMessage(item, defaultRole)
	default:
		return false
	}
}

func (n *sftNormalizer) appendGeminiContent(value any, defaultRole string, pendingByName map[string][]string, syntheticSequence *int) bool {
	content, ok := value.(map[string]any)
	if !ok {
		return false
	}
	role, _ := content["role"].(string)
	if role == "" {
		role = defaultRole
	}
	if role == "model" {
		role = "assistant"
	}
	if role != "user" && role != "assistant" {
		return false
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	texts, calls, results := make([]string, 0, 2), make([]any, 0, 2), make([]map[string]any, 0, 2)
	recognized := false
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if text, exists := part["text"]; exists {
			recognized = true
			value, ok := text.(string)
			if !ok {
				return false
			}
			texts = append(texts, value)
			continue
		}
		callValue := firstMap(part["functionCall"], part["function_call"])
		if callValue != nil {
			recognized = true
			if role != "assistant" {
				return false
			}
			id, _ := callValue["id"].(string)
			name, _ := callValue["name"].(string)
			if id == "" {
				*syntheticSequence++
				id = fmt.Sprintf("gemini_call_%d", *syntheticSequence)
			}
			call, keep, valid := n.canonicalToolCall(id, name, callValue["args"])
			if !valid {
				return false
			}
			if keep {
				calls = append(calls, call)
				pendingByName[name] = append(pendingByName[name], id)
			}
			continue
		}
		resultValue := firstMap(part["functionResponse"], part["function_response"])
		if resultValue == nil || role != "user" {
			return false
		}
		recognized = true
		id, _ := resultValue["id"].(string)
		name, _ := resultValue["name"].(string)
		if id == "" && len(pendingByName[name]) > 0 {
			id = pendingByName[name][0]
			pendingByName[name] = pendingByName[name][1:]
		}
		resultContent, valid := canonicalToolContent(resultValue["response"])
		if !valid || id == "" {
			return false
		}
		if _, duplicate := n.seenResultIDs[id]; !duplicate {
			n.seenResultIDs[id] = struct{}{}
			results = append(results, map[string]any{"role": "tool", "tool_call_id": id, "content": resultContent})
		}
	}
	if len(texts) > 0 || len(calls) > 0 {
		message := map[string]any{"role": role}
		if text := strings.Join(texts, "\n"); text != "" {
			message["content"] = text
		}
		if len(calls) > 0 {
			message["tool_calls"] = calls
		}
		n.messages = append(n.messages, message)
	}
	for _, result := range results {
		n.messages = append(n.messages, result)
	}
	return recognized
}

func (n *sftNormalizer) canonicalOpenAIToolCall(value any) (map[string]any, bool, bool) {
	call, ok := value.(map[string]any)
	if !ok {
		return nil, false, false
	}
	id, _ := call["id"].(string)
	function, ok := call["function"].(map[string]any)
	if !ok {
		return nil, false, false
	}
	name, _ := function["name"].(string)
	return n.canonicalToolCall(id, name, function["arguments"])
}

func (n *sftNormalizer) canonicalToolCall(id, name string, arguments any) (map[string]any, bool, bool) {
	if strings.TrimSpace(name) == "" {
		return nil, false, false
	}
	encoded, ok := canonicalArguments(arguments)
	if !ok {
		return nil, false, false
	}
	if id != "" {
		if _, duplicate := n.seenCallIDs[id]; duplicate {
			return nil, false, true
		}
		n.seenCallIDs[id] = struct{}{}
	}
	call := map[string]any{"type": "function", "function": map[string]any{"name": name, "arguments": encoded}}
	if id != "" {
		call["id"] = id
	}
	return call, true, true
}

func (n *sftNormalizer) appendToolResult(id, content string) bool {
	if id == "" {
		return false
	}
	if _, duplicate := n.seenResultIDs[id]; duplicate {
		return true
	}
	n.seenResultIDs[id] = struct{}{}
	n.messages = append(n.messages, map[string]any{"role": "tool", "tool_call_id": id, "content": content})
	return true
}

func normalizeFunctionTools(value any, protocol string) ([]any, bool) {
	if value == nil {
		return nil, true
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	tools := make([]any, 0, len(items))
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		var function map[string]any
		switch protocol {
		case "openai":
			if item["type"] != "function" {
				return nil, false
			}
			function, ok = item["function"].(map[string]any)
		case "responses":
			if item["type"] != "function" {
				return nil, false
			}
			function = item
		case "anthropic":
			function = item
		default:
			return nil, false
		}
		if !ok && protocol == "openai" {
			return nil, false
		}
		name, _ := function["name"].(string)
		if name == "" {
			return nil, false
		}
		canonical := map[string]any{"name": name}
		if description, ok := function["description"].(string); ok && description != "" {
			canonical["description"] = description
		}
		parameters := function["parameters"]
		if protocol == "anthropic" {
			parameters = function["input_schema"]
		}
		if parameters != nil {
			canonical["parameters"] = parameters
		}
		if strict, ok := function["strict"].(bool); ok {
			canonical["strict"] = strict
		}
		tools = append(tools, map[string]any{"type": "function", "function": canonical})
	}
	return tools, true
}

func normalizeGeminiTools(value any) ([]any, bool) {
	if value == nil {
		return nil, true
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	tools := make([]any, 0, len(items))
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		declarations := item["functionDeclarations"]
		if declarations == nil {
			declarations = item["function_declarations"]
		}
		values, ok := declarations.([]any)
		if !ok {
			return nil, false
		}
		for _, value := range values {
			declaration, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			name, _ := declaration["name"].(string)
			if name == "" {
				return nil, false
			}
			function := map[string]any{"name": name}
			if declaration["parameters"] != nil {
				function["parameters"] = declaration["parameters"]
			}
			if description, ok := declaration["description"].(string); ok && description != "" {
				function["description"] = description
			}
			tools = append(tools, map[string]any{"type": "function", "function": function})
		}
	}
	return tools, true
}

func canonicalTextContent(value any) (string, bool) {
	switch content := value.(type) {
	case nil:
		return "", true
	case string:
		return content, true
	case []any:
		parts := make([]string, 0, len(content))
		for _, value := range content {
			part, ok := value.(map[string]any)
			if !ok {
				return "", false
			}
			typeName, _ := part["type"].(string)
			if typeName != "text" && typeName != "input_text" && typeName != "output_text" {
				return "", false
			}
			text, ok := part["text"].(string)
			if !ok {
				return "", false
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, "\n"), true
	default:
		return "", false
	}
}

func geminiTextContent(value any) (string, bool) {
	parts, ok := value.([]any)
	if !ok {
		return "", false
	}
	texts := make([]string, 0, len(parts))
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok || len(part) != 1 {
			return "", false
		}
		text, ok := part["text"].(string)
		if !ok {
			return "", false
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n"), true
}

func canonicalToolContent(value any) (string, bool) {
	if content, ok := canonicalTextContent(value); ok {
		return content, true
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func canonicalArguments(value any) (string, bool) {
	if value == nil {
		return "{}", true
	}
	if arguments, ok := value.(string); ok {
		if strings.TrimSpace(arguments) == "" {
			return "{}", true
		}
		return arguments, true
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err == nil
}

func firstMap(values ...any) map[string]any {
	for _, value := range values {
		if result, ok := value.(map[string]any); ok {
			return result
		}
	}
	return nil
}

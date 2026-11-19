package gateway

import (
	"bytes"
	"encoding/json"
)

func enrichParsed(p *Parsed, doc map[string]json.RawMessage) {
	p.Specs = parseToolSpecs(doc, p.Protocol)
	switch p.Protocol {
	case ProtoChat, ProtoOllama:
		p.Messages = chatMessagesFull(doc["messages"])
	case ProtoResponses:
		p.Messages = responseMessages(doc["input"])
	case ProtoAnthropic:
		p.Messages = append(systemTurn(jsonText(doc["system"])), anthropicMessages(doc["messages"])...)
	case ProtoGemini:
		p.Messages = append(systemTurn(geminiText(doc["systemInstruction"])), geminiMessages(doc["contents"])...)
	}
}

func withModel(raw []byte, model string) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || model == "" {
		return append([]byte(nil), raw...)
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return append([]byte(nil), raw...)
	}
	doc["model"] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return append([]byte(nil), raw...)
	}
	return out
}

func translatedBody(inbound Parsed, dest Protocol, model string) ([]byte, error) {
	switch dest {
	case ProtoChat, ProtoOllama:
		body := map[string]any{"model": model, "stream": inbound.Stream, "messages": emitChat(inbound.Messages)}
		if specs := emitChatTools(inbound.Specs); specs != nil {
			body["tools"] = specs
		}
		return marshal(body)
	case ProtoResponses:
		body := map[string]any{"model": model, "stream": inbound.Stream, "input": emitResponses(inbound.Messages)}
		if specs := emitResponseTools(inbound.Specs); specs != nil {
			body["tools"] = specs
		}
		return marshal(body)
	case ProtoAnthropic:
		system, messages := emitAnthropic(inbound.Messages)
		body := map[string]any{"model": model, "max_tokens": 4096, "stream": inbound.Stream, "messages": messages}
		if system != "" {
			body["system"] = system
		}
		if specs := emitAnthropicTools(inbound.Specs); specs != nil {
			body["tools"] = specs
		}
		return marshal(body)
	case ProtoGemini:
		system, contents := emitGemini(inbound.Messages)
		body := map[string]any{"contents": contents}
		if system != "" {
			body["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": system}}}
		}
		if specs := emitGeminiTools(inbound.Specs); specs != nil {
			body["tools"] = specs
		}
		return marshal(body)
	default:
		return nil, errTools
	}
}

func paramsOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage(`{}`)
	}
	return raw
}

func argsOrEmpty(args string) string {
	if stringsTrim(args) == "" {
		return "{}"
	}
	return args
}

func stringsTrim(s string) string {
	return string(bytes.TrimSpace([]byte(s)))
}

func parseToolSpecs(doc map[string]json.RawMessage, proto Protocol) []toolSpec {
	raw, ok := doc["tools"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	switch proto {
	case ProtoGemini:
		var tools []struct {
			Decls []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"functionDeclarations"`
		}
		if json.Unmarshal(raw, &tools) != nil {
			return nil
		}
		var out []toolSpec
		for _, tool := range tools {
			for _, decl := range tool.Decls {
				if decl.Name == "" {
					continue
				}
				out = append(out, toolSpec{Name: decl.Name, Description: decl.Description, Parameters: paramsOrEmpty(decl.Parameters)})
			}
		}
		return out
	case ProtoAnthropic:
		var tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(raw, &tools) != nil {
			return nil
		}
		var out []toolSpec
		for _, tool := range tools {
			if tool.Name == "" {
				continue
			}
			out = append(out, toolSpec{Name: tool.Name, Description: tool.Description, Parameters: paramsOrEmpty(tool.Schema)})
		}
		return out
	default:
		var tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
			Function    *struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &tools) != nil {
			return nil
		}
		var out []toolSpec
		for _, tool := range tools {
			spec := toolSpec{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}
			if tool.Function != nil {
				spec = toolSpec{Name: tool.Function.Name, Description: tool.Function.Description, Parameters: tool.Function.Parameters}
			}
			if spec.Name == "" {
				continue
			}
			spec.Parameters = paramsOrEmpty(spec.Parameters)
			out = append(out, spec)
		}
		return out
	}
}

func chatMessagesFull(raw json.RawMessage) []turn {
	var rows []struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content"`
		Reasoning        string          `json:"reasoning"`
		ToolCallID       string          `json:"tool_call_id"`
		Name             string          `json:"name"`
		ToolCalls        []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	out := make([]turn, 0, len(rows))
	for _, row := range rows {
		msg := turn{Role: row.Role, Text: partText(row.Content)}
		if msg.Role == "" {
			msg.Role = "user"
		}
		if row.ReasoningContent != "" {
			msg.Reasoning = row.ReasoningContent
		} else {
			msg.Reasoning = row.Reasoning
		}
		for _, call := range row.ToolCalls {
			msg.Calls = append(msg.Calls, toolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
		if msg.Role == "tool" || row.ToolCallID != "" {
			msg.Role = "tool"
			msg.Result = &toolResult{ID: row.ToolCallID, Name: row.Name, Content: msg.Text}
		}
		if msg.Text == "" && msg.Reasoning == "" && len(msg.Calls) == 0 && msg.Result == nil {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func anthropicMessages(raw json.RawMessage) []turn {
	var rows []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	out := make([]turn, 0, len(rows))
	for _, row := range rows {
		msg := turn{Role: row.Role, Text: partText(row.Content)}
		if msg.Role == "" {
			msg.Role = "user"
		}
		var blocks []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(row.Content, &blocks)
		for _, block := range blocks {
			switch block.Type {
			case "thinking":
				msg.Reasoning += block.Thinking
			case "tool_use":
				args := ""
				if len(block.Input) > 0 {
					args = string(block.Input)
				}
				msg.Calls = append(msg.Calls, toolCall{ID: block.ID, Name: block.Name, Arguments: args})
			case "tool_result":
				msg.Result = &toolResult{ID: block.ToolUseID, Content: partText(block.Content)}
				if msg.Result.Content == "" {
					msg.Result.Content = jsonString(block.Content)
				}
			}
		}
		if msg.Text == "" && msg.Reasoning == "" && len(msg.Calls) == 0 && msg.Result == nil {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func geminiMessages(raw json.RawMessage) []turn {
	var rows []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text     string `json:"text"`
			Thought  bool   `json:"thought"`
			Function *struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			} `json:"functionCall"`
			Response *struct {
				Name     string          `json:"name"`
				Response json.RawMessage `json:"response"`
			} `json:"functionResponse"`
		} `json:"parts"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	out := make([]turn, 0, len(rows))
	for _, row := range rows {
		msg := turn{Role: "user"}
		if row.Role == "model" || row.Role == "assistant" {
			msg.Role = "assistant"
		}
		for _, part := range row.Parts {
			if part.Thought {
				msg.Reasoning += part.Text
				continue
			}
			if part.Function != nil && part.Function.Name != "" {
				args := ""
				if len(part.Function.Args) > 0 {
					args = string(part.Function.Args)
				}
				msg.Calls = append(msg.Calls, toolCall{Name: part.Function.Name, Arguments: args})
				continue
			}
			if part.Response != nil {
				content := string(part.Response.Response)
				msg.Result = &toolResult{Name: part.Response.Name, Content: content}
				continue
			}
			msg.Text += part.Text
		}
		if msg.Text == "" && msg.Reasoning == "" && len(msg.Calls) == 0 && msg.Result == nil {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func responseMessages(raw json.RawMessage) []turn {
	if s := jsonString(raw); s != "" {
		return []turn{{Role: "user", Text: s}}
	}
	var rows []map[string]json.RawMessage
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	typed := false
	for _, row := range rows {
		if _, ok := row["type"]; ok {
			if _, hasRole := row["role"]; !hasRole {
				typed = true
				break
			}
		}
	}
	if !typed {
		return chatMessagesFull(raw)
	}
	var out []turn
	for _, row := range rows {
		kind := jsonString(row["type"])
		switch kind {
		case "function_call":
			out = append(out, turn{Role: "assistant", Calls: []toolCall{{
				ID: jsonString(row["call_id"]), Name: jsonString(row["name"]), Arguments: jsonString(row["arguments"]),
			}}})
		case "function_call_output":
			out = append(out, turn{Role: "tool", Result: &toolResult{ID: jsonString(row["call_id"]), Content: jsonString(row["output"])}})
		case "reasoning":
			out = append(out, turn{Role: "assistant", Reasoning: partText(row["summary"])})
		default:
			text := partText(row["content"])
			if text == "" {
				text = jsonString(row["content"])
			}
			if text == "" {
				continue
			}
			role := jsonString(row["role"])
			if role == "" {
				role = "user"
			}
			out = append(out, turn{Role: role, Text: text})
		}
	}
	return out
}

func emitChatTools(specs []toolSpec) []any {
	if len(specs) == 0 {
		return nil
	}
	out := make([]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": spec.Name, "description": spec.Description, "parameters": json.RawMessage(paramsOrEmpty(spec.Parameters)),
			},
		})
	}
	return out
}

func emitResponseTools(specs []toolSpec) []any {
	if len(specs) == 0 {
		return nil
	}
	out := make([]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"type": "function", "name": spec.Name, "description": spec.Description,
			"parameters": json.RawMessage(paramsOrEmpty(spec.Parameters)),
		})
	}
	return out
}

func emitAnthropicTools(specs []toolSpec) []any {
	if len(specs) == 0 {
		return nil
	}
	out := make([]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"name": spec.Name, "description": spec.Description, "input_schema": json.RawMessage(paramsOrEmpty(spec.Parameters)),
		})
	}
	return out
}

func emitGeminiTools(specs []toolSpec) []any {
	if len(specs) == 0 {
		return nil
	}
	decls := make([]any, 0, len(specs))
	for _, spec := range specs {
		decls = append(decls, map[string]any{
			"name": spec.Name, "description": spec.Description, "parameters": json.RawMessage(paramsOrEmpty(spec.Parameters)),
		})
	}
	return []any{map[string]any{"functionDeclarations": decls}}
}

func emitChat(messages []turn) []any {
	if len(messages) == 0 {
		return []any{map[string]string{"role": "user", "content": ""}}
	}
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		if msg.Result != nil {
			out = append(out, map[string]string{"role": "tool", "tool_call_id": msg.Result.ID, "name": msg.Result.Name, "content": msg.Result.Content})
			continue
		}
		row := map[string]any{"role": msg.Role, "content": msg.Text}
		if msg.Reasoning != "" {
			row["reasoning_content"] = msg.Reasoning
		}
		if len(msg.Calls) > 0 {
			calls := make([]any, 0, len(msg.Calls))
			for _, call := range msg.Calls {
				calls = append(calls, map[string]any{
					"id": call.ID, "type": "function",
					"function": map[string]string{"name": call.Name, "arguments": argsOrEmpty(call.Arguments)},
				})
			}
			row["tool_calls"] = calls
		}
		out = append(out, row)
	}
	return out
}

func emitResponses(messages []turn) []any {
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		if msg.Reasoning != "" {
			out = append(out, map[string]any{"type": "reasoning", "summary": []map[string]string{{"type": "summary_text", "text": msg.Reasoning}}})
		}
		if msg.Result != nil {
			out = append(out, map[string]string{"type": "function_call_output", "call_id": msg.Result.ID, "output": msg.Result.Content})
			continue
		}
		for _, call := range msg.Calls {
			out = append(out, map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": argsOrEmpty(call.Arguments)})
		}
		if msg.Text != "" || (msg.Reasoning == "" && len(msg.Calls) == 0) {
			role := msg.Role
			if role == "" {
				role = "user"
			}
			out = append(out, map[string]string{"role": role, "content": msg.Text})
		}
	}
	if len(out) == 0 {
		out = append(out, map[string]string{"role": "user", "content": ""})
	}
	return out
}

func emitAnthropic(messages []turn) (string, []any) {
	var system string
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" {
			system += msg.Text
			continue
		}
		role := "user"
		if msg.Role == "assistant" {
			role = "assistant"
		}
		var blocks []any
		if msg.Reasoning != "" && role == "assistant" {
			blocks = append(blocks, map[string]string{"type": "thinking", "thinking": msg.Reasoning})
		}
		if msg.Text != "" {
			blocks = append(blocks, map[string]string{"type": "text", "text": msg.Text})
		}
		for _, call := range msg.Calls {
			var input any = map[string]any{}
			if json.Valid([]byte(call.Arguments)) && len(bytes.TrimSpace([]byte(call.Arguments))) > 0 {
				input = json.RawMessage(call.Arguments)
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
		}
		if msg.Result != nil {
			role = "user"
			blocks = append(blocks, map[string]string{"type": "tool_result", "tool_use_id": msg.Result.ID, "content": msg.Result.Content})
		}
		if len(blocks) == 0 {
			blocks = append(blocks, map[string]string{"type": "text", "text": ""})
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": ""}}})
	}
	return system, out
}

func emitGemini(messages []turn) (string, []any) {
	var system string
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" {
			system += msg.Text
			continue
		}
		role := "user"
		if msg.Role == "assistant" {
			role = "model"
		}
		var parts []any
		if msg.Reasoning != "" {
			parts = append(parts, map[string]any{"text": msg.Reasoning, "thought": true})
		}
		if msg.Text != "" || (len(msg.Calls) == 0 && msg.Result == nil && msg.Reasoning == "") {
			parts = append(parts, map[string]string{"text": msg.Text})
		}
		for _, call := range msg.Calls {
			var args any = map[string]any{}
			if json.Valid([]byte(call.Arguments)) && stringsTrim(call.Arguments) != "" {
				args = json.RawMessage(call.Arguments)
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"name": call.Name, "args": args}})
		}
		if msg.Result != nil {
			var response any = map[string]any{}
			if json.Valid([]byte(msg.Result.Content)) && stringsTrim(msg.Result.Content) != "" && msg.Result.Content[0] == '{' {
				response = json.RawMessage(msg.Result.Content)
			} else {
				response = map[string]string{"result": msg.Result.Content}
			}
			parts = append(parts, map[string]any{"functionResponse": map[string]any{"name": msg.Result.Name, "response": response}})
		}
		out = append(out, map[string]any{"role": role, "parts": parts})
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"role": "user", "parts": []map[string]string{{"text": ""}}})
	}
	return system, out
}

type replyView struct {
	text      string
	reasoning string
	calls     []toolCall
}

func readReply(proto Protocol, body []byte) replyView {
	view := replyView{text: replyText(proto, body)}
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return view
	}
	switch proto {
	case ProtoChat:
		var choices []struct {
			Message struct {
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		}
		_ = json.Unmarshal(doc["choices"], &choices)
		if len(choices) == 0 {
			return view
		}
		view.reasoning = choices[0].Message.ReasoningContent
		if view.reasoning == "" {
			view.reasoning = choices[0].Message.Reasoning
		}
		for _, call := range choices[0].Message.ToolCalls {
			view.calls = append(view.calls, toolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
	case ProtoAnthropic:
		var blocks []struct {
			Type     string          `json:"type"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(doc["content"], &blocks)
		for _, block := range blocks {
			if block.Type == "thinking" {
				view.reasoning += block.Thinking
			}
			if block.Type == "tool_use" {
				view.calls = append(view.calls, toolCall{ID: block.ID, Name: block.Name, Arguments: string(block.Input)})
			}
		}
	case ProtoResponses:
		var output []struct {
			Type      string          `json:"type"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
			Summary   json.RawMessage `json:"summary"`
		}
		_ = json.Unmarshal(doc["output"], &output)
		for _, item := range output {
			if item.Type == "reasoning" {
				view.reasoning += partText(item.Summary)
			}
			if item.Type == "function_call" {
				view.calls = append(view.calls, toolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments})
			}
		}
	case ProtoGemini:
		var candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
					Call    *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		}
		_ = json.Unmarshal(doc["candidates"], &candidates)
		for _, cand := range candidates {
			for _, part := range cand.Content.Parts {
				if part.Thought {
					view.reasoning += part.Text
				}
				if part.Call != nil && part.Call.Name != "" {
					view.calls = append(view.calls, toolCall{Name: part.Call.Name, Arguments: string(part.Call.Args)})
				}
			}
		}
	case ProtoOllama:
		var msg struct {
			Thinking  string `json:"thinking"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments any    `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		_ = json.Unmarshal(doc["message"], &msg)
		view.reasoning = msg.Thinking
		for _, call := range msg.ToolCalls {
			args := ""
			switch v := call.Function.Arguments.(type) {
			case string:
				args = v
			default:
				if v != nil {
					b, _ := json.Marshal(v)
					args = string(b)
				}
			}
			view.calls = append(view.calls, toolCall{Name: call.Function.Name, Arguments: args})
		}
	}
	return view
}

func renderReply(proto Protocol, model string, view replyView) []byte {
	if view.reasoning == "" && len(view.calls) == 0 {
		return clientBody(proto, model, view.text)
	}
	switch proto {
	case ProtoAnthropic:
		var blocks []any
		if view.reasoning != "" {
			blocks = append(blocks, map[string]string{"type": "thinking", "thinking": view.reasoning})
		}
		if view.text != "" {
			blocks = append(blocks, map[string]string{"type": "text", "text": view.text})
		}
		for _, call := range view.calls {
			var input any = map[string]any{}
			if json.Valid([]byte(call.Arguments)) && stringsTrim(call.Arguments) != "" {
				input = json.RawMessage(call.Arguments)
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
		}
		b, _ := json.Marshal(map[string]any{
			"id": "msg_aigate", "type": "message", "role": "assistant", "model": model,
			"content": blocks, "stop_reason": anthropicStop(view),
		})
		return b
	case ProtoResponses:
		var output []any
		if view.reasoning != "" {
			output = append(output, map[string]any{"type": "reasoning", "summary": []map[string]string{{"type": "summary_text", "text": view.reasoning}}})
		}
		if view.text != "" {
			output = append(output, map[string]any{
				"type": "message", "role": "assistant",
				"content": []map[string]string{{"type": "output_text", "text": view.text}},
			})
		}
		for _, call := range view.calls {
			output = append(output, map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": argsOrEmpty(call.Arguments)})
		}
		b, _ := json.Marshal(map[string]any{
			"id": "resp_aigate", "object": "response", "status": "completed", "model": model, "output": output,
		})
		return b
	case ProtoGemini:
		var parts []any
		if view.reasoning != "" {
			parts = append(parts, map[string]any{"text": view.reasoning, "thought": true})
		}
		if view.text != "" {
			parts = append(parts, map[string]string{"text": view.text})
		}
		for _, call := range view.calls {
			var args any = map[string]any{}
			if json.Valid([]byte(call.Arguments)) && stringsTrim(call.Arguments) != "" {
				args = json.RawMessage(call.Arguments)
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"name": call.Name, "args": args}})
		}
		b, _ := json.Marshal(map[string]any{"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": parts}}}})
		return b
	case ProtoOllama:
		msg := map[string]any{"role": "assistant", "content": view.text}
		if view.reasoning != "" {
			msg["thinking"] = view.reasoning
		}
		if len(view.calls) > 0 {
			calls := make([]any, 0, len(view.calls))
			for _, call := range view.calls {
				var args any = map[string]any{}
				if json.Valid([]byte(call.Arguments)) {
					args = json.RawMessage(call.Arguments)
				}
				calls = append(calls, map[string]any{"function": map[string]any{"name": call.Name, "arguments": args}})
			}
			msg["tool_calls"] = calls
		}
		b, _ := json.Marshal(map[string]any{"model": model, "done": true, "message": msg})
		return b
	default:
		message := map[string]any{"role": "assistant", "content": view.text}
		if view.reasoning != "" {
			message["reasoning_content"] = view.reasoning
		}
		if len(view.calls) > 0 {
			calls := make([]any, 0, len(view.calls))
			for _, call := range view.calls {
				calls = append(calls, map[string]any{
					"id": call.ID, "type": "function",
					"function": map[string]string{"name": call.Name, "arguments": argsOrEmpty(call.Arguments)},
				})
			}
			message["tool_calls"] = calls
		}
		b, _ := json.Marshal(map[string]any{
			"id": "chatcmpl_aigate", "object": "chat.completion", "model": model,
			"choices": []map[string]any{{"index": 0, "finish_reason": stopReason(view), "message": message}},
		})
		return b
	}
}

func stopReason(view replyView) string {
	if len(view.calls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func anthropicStop(view replyView) string {
	if len(view.calls) > 0 {
		return "tool_use"
	}
	return "end_turn"
}

type streamPiece struct {
	text      string
	reasoning string
	call      *toolCall
}

func (p streamPiece) empty() bool {
	return p.text == "" && p.reasoning == "" && p.call == nil
}

func eventPieces(proto Protocol, data []byte) streamPiece {
	piece := streamPiece{text: deltaText(proto, data), reasoning: deltaReasoning(proto, data)}
	if call, ok := deltaCall(proto, data); ok {
		piece.call = &call
	}
	return piece
}

func deltaReasoning(proto Protocol, data []byte) string {
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	switch proto {
	case ProtoChat:
		var choices []struct {
			Delta struct {
				Reasoning string `json:"reasoning_content"`
			} `json:"delta"`
		}
		_ = json.Unmarshal(doc["choices"], &choices)
		if len(choices) == 0 {
			return ""
		}
		return choices[0].Delta.Reasoning
	case ProtoAnthropic:
		var delta struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		}
		_ = json.Unmarshal(doc["delta"], &delta)
		if delta.Type == "thinking_delta" {
			return delta.Thinking
		}
	case ProtoGemini:
		return thoughtText(doc["candidates"])
	}
	return ""
}

func thoughtText(raw json.RawMessage) string {
	var candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &candidates)
	var b bytes.Buffer
	for _, cand := range candidates {
		for _, part := range cand.Content.Parts {
			if part.Thought {
				b.WriteString(part.Text)
			}
		}
	}
	return b.String()
}

func deltaCall(proto Protocol, data []byte) (toolCall, bool) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return toolCall{}, false
	}
	switch proto {
	case ProtoChat:
		var choices []struct {
			Delta struct {
				Calls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		}
		_ = json.Unmarshal(doc["choices"], &choices)
		if len(choices) == 0 || len(choices[0].Delta.Calls) == 0 {
			return toolCall{}, false
		}
		call := choices[0].Delta.Calls[0]
		if call.Function.Name == "" && call.Function.Arguments == "" && call.ID == "" {
			return toolCall{}, false
		}
		return toolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments}, true
	case ProtoAnthropic:
		var block struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(doc["content_block"], &block)
		if block.Type == "tool_use" && block.Name != "" {
			return toolCall{ID: block.ID, Name: block.Name}, true
		}
		var delta struct {
			Type    string `json:"type"`
			Partial string `json:"partial_json"`
		}
		_ = json.Unmarshal(doc["delta"], &delta)
		if delta.Type == "input_json_delta" && delta.Partial != "" {
			return toolCall{Arguments: delta.Partial}, true
		}
	}
	return toolCall{}, false
}

func clientPieces(proto Protocol, model string, piece streamPiece) [][]byte {
	var out [][]byte
	if piece.reasoning != "" {
		out = append(out, reasoningEvent(proto, model, piece.reasoning))
	}
	if piece.text != "" {
		out = append(out, clientEvent(proto, model, piece.text, false))
	}
	if piece.call != nil {
		if ev := toolEvent(proto, model, *piece.call); ev != nil {
			out = append(out, ev)
		}
	}
	return out
}

func reasoningEvent(proto Protocol, model, text string) []byte {
	switch proto {
	case ProtoAnthropic:
		b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "thinking_delta", "thinking": text}})
		return b
	case ProtoGemini:
		b, _ := json.Marshal(map[string]any{"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": []map[string]any{{"text": text, "thought": true}}}}}})
		return b
	default:
		b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk", "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]string{"reasoning_content": text}}}})
		return b
	}
}

func toolEvent(proto Protocol, model string, call toolCall) []byte {
	switch proto {
	case ProtoAnthropic:
		if call.Name != "" {
			b, _ := json.Marshal(map[string]any{"type": "content_block_start", "content_block": map[string]string{"type": "tool_use", "id": call.ID, "name": call.Name}})
			return b
		}
		if call.Arguments != "" {
			b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "input_json_delta", "partial_json": call.Arguments}})
			return b
		}
	case ProtoChat, ProtoOllama:
		b, _ := json.Marshal(map[string]any{
			"object": "chat.completion.chunk", "model": model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": call.ID, "type": "function",
				"function": map[string]string{"name": call.Name, "arguments": call.Arguments},
			}}}}},
		})
		return b
	}
	return nil
}

// Package fakeopenai is a minimal in-process OpenAI-compatible chat
// server for tests: it answers /v1/chat/completions (and /v1/models)
// with a scripted reply, so BYOK code paths can be exercised without a
// real provider or any network egress.
package fakeopenai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Server is a fake OpenAI-compatible endpoint. Its BaseURL (with the
// /v1 suffix) is what you pass as a BYOK provider base URL.
type Server struct {
	ts    *httptest.Server
	reply string

	mu         sync.Mutex
	requests   []Request
	apiKey     string // if set, requests must present it as a Bearer token
	toolMatch  string // WithToolCall: substring of the tool name to invoke
	toolArgs   string // WithToolCall: JSON arguments for the invocation
	toolCalled bool   // the scripted tool call was already issued
}

// Request records one captured chat-completions call.
type Request struct {
	Model    string
	Messages []Message
	Auth     string
}

// Message is a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Option configures the server.
type Option func(*Server)

// WithReply sets the assistant content returned for every completion.
func WithReply(reply string) Option { return func(s *Server) { s.reply = reply } }

// WithAPIKey requires callers to present the given key as a Bearer
// token; requests without it get 401.
func WithAPIKey(key string) Option { return func(s *Server) { s.apiKey = key } }

// WithToolCall scripts one tool invocation: the first completion whose
// request advertises a tool with match in its name answers with a call
// to that tool using args (a JSON object); every later completion
// returns the normal reply. This drives a real client's execute-tool →
// report-result loop without a live model.
func WithToolCall(match, args string) Option {
	return func(s *Server) { s.toolMatch, s.toolArgs = match, args }
}

// New starts a fake server. Call Close when done.
func New(opts ...Option) *Server {
	s := &Server{reply: "ok"}
	for _, o := range opts {
		o(s)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	s.ts = httptest.NewServer(mux)
	return s
}

// BaseURL is the provider base URL to hand to a BYOK config
// (…/v1).
func (s *Server) BaseURL() string { return s.ts.URL + "/v1" }

// Close shuts the server down.
func (s *Server) Close() { s.ts.Close() }

// Requests returns the captured chat-completions calls.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "fake-model", "object": "model", "owned_by": "fakeopenai"},
		},
	})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if s.apiKey != "" && auth != "Bearer "+s.apiKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]any{"message": "invalid api key", "type": "invalid_request_error"},
		})
		return
	}
	var body struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
		Stream   bool      `json:"stream"`
		Tools    []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "invalid_request_error"},
		})
		return
	}

	s.mu.Lock()
	s.requests = append(s.requests, Request{Model: body.Model, Messages: body.Messages, Auth: auth})
	tool := ""
	if s.toolMatch != "" && !s.toolCalled {
		for _, t := range body.Tools {
			if strings.Contains(t.Function.Name, s.toolMatch) {
				tool, s.toolCalled = t.Function.Name, true
				break
			}
		}
	}
	s.mu.Unlock()

	if tool != "" {
		if body.Stream {
			s.streamToolCall(w, body.Model, tool)
			return
		}
		writeJSON(w, http.StatusOK, s.toolCompletion(body.Model, tool))
		return
	}
	if body.Stream {
		s.streamChat(w, body.Model)
		return
	}
	writeJSON(w, http.StatusOK, s.completion(body.Model))
}

// toolCall is the scripted invocation in OpenAI's tool_calls shape.
func (s *Server) toolCall(tool string) map[string]any {
	return map[string]any{
		"id":   "call_fake_1",
		"type": "function",
		"function": map[string]any{
			"name":      tool,
			"arguments": s.toolArgs,
		},
	}
}

func (s *Server) toolCompletion(model, tool string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": []map[string]any{s.toolCall(tool)},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{
			"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12,
		},
	}
}

// streamToolCall emits the invocation as one SSE delta plus the
// tool_calls finish chunk (mirrors streamChat's shape).
func (s *Server) streamToolCall(w http.ResponseWriter, model, tool string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusOK, s.toolCompletion(model, tool))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	chunk := func(delta map[string]any, finish any) {
		payload := map[string]any{
			"id":      "chatcmpl-fake",
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		b, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	call := s.toolCall(tool)
	call["index"] = 0
	chunk(map[string]any{"role": "assistant"}, nil)
	chunk(map[string]any{"tool_calls": []map[string]any{call}}, nil)
	chunk(map[string]any{}, "tool_calls")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (s *Server) completion(model string) map[string]any {
	promptToks, replyToks := 8, len(strings.Fields(s.reply))
	return map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": s.reply},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptToks,
			"completion_tokens": replyToks,
			"total_tokens":      promptToks + replyToks,
		},
	}
}

// streamChat emits a minimal SSE stream: role + content deltas, the
// closing finish_reason chunk (consumers refuse to finalize a stream
// without one), then [DONE].
func (s *Server) streamChat(w http.ResponseWriter, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusOK, s.completion(model))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	chunk := func(delta map[string]any, finish any) {
		payload := map[string]any{
			"id":      "chatcmpl-fake",
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		b, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	chunk(map[string]any{"role": "assistant"}, nil)
	chunk(map[string]any{"content": s.reply}, nil)
	chunk(map[string]any{}, "stop")
	// streamed usage arrives in a trailing chunk with no choices (the
	// stream_options.include_usage shape); send it unconditionally so
	// metering works the same as the non-streaming path.
	promptToks, replyToks := 8, len(strings.Fields(s.reply))
	usage, _ := json.Marshal(map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion.chunk",
		"model":   model,
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":     promptToks,
			"completion_tokens": replyToks,
			"total_tokens":      promptToks + replyToks,
		},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", usage)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

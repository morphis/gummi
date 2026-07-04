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

	mu       sync.Mutex
	requests []Request
	apiKey   string // if set, requests must present it as a Bearer token
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "invalid_request_error"},
		})
		return
	}

	s.mu.Lock()
	s.requests = append(s.requests, Request{Model: body.Model, Messages: body.Messages, Auth: auth})
	s.mu.Unlock()

	if body.Stream {
		s.streamChat(w, body.Model)
		return
	}
	writeJSON(w, http.StatusOK, s.completion(body.Model))
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

// streamChat emits a minimal SSE stream: one content delta then [DONE].
func (s *Server) streamChat(w http.ResponseWriter, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusOK, s.completion(model))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	chunk := func(delta map[string]any) {
		payload := map[string]any{
			"id":      "chatcmpl-fake",
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta}},
		}
		b, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	chunk(map[string]any{"role": "assistant"})
	chunk(map[string]any{"content": s.reply})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

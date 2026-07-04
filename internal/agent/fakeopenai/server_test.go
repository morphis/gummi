package fakeopenai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func post(t *testing.T, url, key string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestChatCompletion(t *testing.T) {
	s := New(WithReply("four score and seven"))
	defer s.Close()

	resp := post(t, s.BaseURL()+"/chat/completions", "", map[string]any{
		"model":    "fake-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Choices[0].Message.Content != "four score and seven" {
		t.Errorf("content = %q", out.Choices[0].Message.Content)
	}
	if out.Usage.CompletionTokens != 4 {
		t.Errorf("completion tokens = %d, want 4", out.Usage.CompletionTokens)
	}
	if reqs := s.Requests(); len(reqs) != 1 || reqs[0].Model != "fake-model" {
		t.Errorf("captured requests wrong: %+v", reqs)
	}
}

func TestAPIKeyEnforced(t *testing.T) {
	s := New(WithReply("ok"), WithAPIKey("secret-key"))
	defer s.Close()

	bad := post(t, s.BaseURL()+"/chat/completions", "wrong", map[string]any{"model": "m", "messages": []Message{}})
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", bad.StatusCode)
	}
	good := post(t, s.BaseURL()+"/chat/completions", "secret-key", map[string]any{"model": "m", "messages": []Message{}})
	good.Body.Close()
	if good.StatusCode != http.StatusOK {
		t.Fatalf("right key: status = %d, want 200", good.StatusCode)
	}
}

func TestStreaming(t *testing.T) {
	s := New(WithReply("streamed reply"))
	defer s.Close()
	resp := post(t, s.BaseURL()+"/chat/completions", "", map[string]any{
		"model":    "m",
		"messages": []Message{{Role: "user", Content: "hi"}},
		"stream":   true,
	})
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	var content strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sawDone := false
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta map[string]any `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatal(err)
		}
		if c, ok := chunk.Choices[0].Delta["content"].(string); ok {
			content.WriteString(c)
		}
	}
	if !sawDone {
		t.Error("stream did not terminate with [DONE]")
	}
	if content.String() != "streamed reply" {
		t.Errorf("streamed content = %q", content.String())
	}
}

func TestModelsEndpoint(t *testing.T) {
	s := New()
	defer s.Close()
	resp := post(t, s.BaseURL()+"/models", "", nil)
	// /models is GET in real APIs but our handler ignores method; the
	// test just checks it answers with the fake model listed.
	defer resp.Body.Close()
	var out struct {
		Data []struct{ ID string } `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Data) != 1 || out.Data[0].ID != "fake-model" {
		t.Errorf("models = %+v", out.Data)
	}
}

package agent

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestAssistantImageBlockGoesThroughTheSink(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"content": []map[string]any{{
		"type":   "image",
		"source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString([]byte("png-bytes"))},
	}}})
	var got []byte
	s := &claudeSession{imageSink: func(b []byte, _ string) string { got = b; return "![image](.gummi/attachments/x.png)" }}
	evs := s.mapAssistant(raw)
	if len(evs) != 1 || evs[0].Kind != EventMessage || evs[0].Text != "![image](.gummi/attachments/x.png)" || string(got) != "png-bytes" {
		t.Fatalf("events = %+v, sink got %q", evs, got)
	}
	s.imageSink = nil
	if evs := s.mapAssistant(raw); len(evs) != 0 {
		t.Fatalf("no sink must drop the image, got %+v", evs)
	}
}

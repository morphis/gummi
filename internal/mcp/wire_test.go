package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

// encode/decode round-trip the four frame shapes the transport carries.
func TestFrameRoundTrip(t *testing.T) {
	req := &Request{JSONRPC: JSONRPC, ID: json.RawMessage(`"1"`), Method: "tools/list"}
	var buf bytes.Buffer
	if err := Encode(&buf, req); err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.TrimSpace(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "tools/list" || string(got.ID) != `"1"` {
		t.Fatalf("decode = %+v, want method tools/list id \"1\"", got)
	}

	// a notification carries no id
	buf.Reset()
	if err := Encode(&buf, &Request{JSONRPC: JSONRPC, Method: "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	got, err = Decode(bytes.TrimSpace(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ID) != 0 {
		t.Fatalf("notification decoded with id %q, want none", got.ID)
	}
}

// a response and an error object both encode as a single newline-terminated line.
func TestFrameLineDelimited(t *testing.T) {
	resp := &Response{JSONRPC: JSONRPC, ID: json.RawMessage(`2`), Result: json.RawMessage(`{"ok":true}`)}
	var buf bytes.Buffer
	if err := Encode(&buf, resp); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(buf.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("response has %d newlines, want exactly 1", bytes.Count(buf.Bytes(), []byte{'\n'}))
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte{'\n'}) {
		t.Fatalf("response not newline-terminated: %q", buf.Bytes())
	}
}

// a malformed or wrong-version frame is rejected by decode.
func TestDecodeRejectsBadFrame(t *testing.T) {
	for _, line := range [][]byte{
		[]byte(`{"method":"tools/list"}`),               // no jsonrpc version
		[]byte(`not json`),                              // unparseable
		[]byte(`{"jsonrpc":"1.0","id":1,"method":"x"}`), // wrong version
	} {
		if _, err := Decode(line); err == nil {
			t.Fatalf("Decode(%q) succeeded, want error", line)
		}
	}
}

func TestEncodeWriteError(t *testing.T) {
	// a failing writer surfaces the write error from encode.
	if err := Encode(failWriter{}, &Response{}); err == nil {
		t.Fatal("encode to failing writer succeeded, want error")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

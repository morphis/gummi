package livelog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"
)

// tailBytes is how much of a live file's end Stat reads to find the last
// record. Records are small; a few KB always spans several of them, and
// reading a fixed tail keeps Stat cheap enough to call per board row.
const tailBytes = 8 << 10

// Status is a cheap summary of a card's live file: who is driving it,
// what they are driving, and whether the session already ended. It is
// what a board row or a `watch` header needs, without following the
// stream.
type Status struct {
	// PID owns the session — the process that opened the file. Combine it
	// with a liveness probe (state.ProcessAlive) to tell a live run from
	// the file a finished one left behind.
	PID int
	// Feature/Stage/Role/Agent/Model come from the header record.
	Feature string
	Stage   string
	Role    string
	Agent   string
	Model   string
	// Started is the header record's timestamp; Updated is the file's
	// last write, which is how a follower spots a quiet run.
	Started time.Time
	Updated time.Time
	// Stopped is true once the session wrote its terminal record. A
	// stopped file is history: its owner may still be alive (a board that
	// finished a stage), but nothing further will arrive.
	Stopped bool
}

// Stat summarizes the live file at path without following it. It returns
// ErrNoLiveFile when nothing has driven the card in a process that writes
// one, so a caller can fail loud rather than wait on a stream that may
// never start.
func Stat(path string) (Status, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Status{}, ErrNoLiveFile
		}
		return Status{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Status{}, err
	}
	var st Status
	st.Updated = fi.ModTime()

	// the header is the first line: it carries the session's identity.
	head := bufio.NewReader(f)
	line, err := head.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Status{}, ErrNoLiveFile // created but not yet written
	}
	var hdr Record
	if err := json.Unmarshal(bytes.TrimSpace(line), &hdr); err != nil {
		return Status{}, ErrNoLiveFile // not a live file we can read
	}
	st.PID, st.Feature, st.Stage = hdr.PID, hdr.Feature, hdr.Stage
	st.Role, st.Agent, st.Model, st.Started = hdr.Role, hdr.Agent, hdr.Model, hdr.Time

	if last, ok := lastRecord(f, fi.Size()); ok {
		st.Stopped = last.Kind == KindStopped
	}
	return st, nil
}

// lastRecord returns the final complete record in f, reading only the
// file's tail. A seek lands mid-line, so everything before the first
// newline is discarded — the rest are whole records, and the last one
// wins.
func lastRecord(f *os.File, size int64) (Record, bool) {
	start := size - tailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return Record{}, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return Record{}, false
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			return Record{}, false // one record spans the whole tail
		}
	}
	var last Record
	found := false
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		last, found = r, true
	}
	return last, found
}

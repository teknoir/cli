package cmd

// The capture store of one MCP server: a sample too large to return inline is written to one file
// and read back a page at a time by mqtt_read. A capture is <dir>/<id>.jsonl, one JSON line per
// message, and lives 30 minutes.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// How long one capture lives and how many bytes of captures the store keeps. Variables so a test
// can shorten them.
var (
	mcpCaptureTTL      = 30 * time.Minute
	mcpCaptureStoreMax = 64 << 20
)

// mcpCaptureIDLen is the hex part of a capture's id: cap- and 12 hex characters, nothing else.
const mcpCaptureIDLen = 12

// mcpCapture is one capture the store holds.
type mcpCapture struct {
	file    string
	created time.Time
	expires time.Time
	count   int
	total   int
}

// mcpCaptureStore is the captures of one MCP server. Its mutex guards the map: two tool calls may
// run at once.
type mcpCaptureStore struct {
	dir  string
	mu   sync.Mutex
	caps map[string]*mcpCapture
	once sync.Once // the first use sweeps what an earlier process left behind
}

// newCaptureStore keeps captures in dir.
func newCaptureStore(dir string) *mcpCaptureStore {
	return &mcpCaptureStore{dir: dir, caps: map[string]*mcpCapture{}}
}

// mcpStdioCaptureDir is where the stdio server keeps its captures: the temp dir's tnctl-captures.
// A variable so a test keeps its captures in its own directory.
var mcpStdioCaptureDir = func() string { return filepath.Join(os.TempDir(), "tnctl-captures") }

// validCaptureID reports whether id is cap- and 12 hex characters. It is checked before any path is
// built from an id: a tool's input is never a file name.
func validCaptureID(id string) bool {
	if !strings.HasPrefix(id, "cap-") || len(id) != len("cap-")+mcpCaptureIDLen {
		return false
	}
	_, err := hex.DecodeString(id[len("cap-"):])
	return err == nil
}

// sweep removes the captures an earlier process left behind: cap-*.jsonl files older than the TTL,
// and only those. Another process may be writing one right now, but a file that old is done with.
func (s *mcpCaptureStore) sweep() {
	s.once.Do(func() {
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			return // the dir does not exist until the first capture
		}
		cutoff := time.Now().Add(-mcpCaptureTTL)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".jsonl") || !validCaptureID(strings.TrimSuffix(name, ".jsonl")) {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	})
}

// writeCapture writes one new capture: lines hands each message to w as it comes, so a 200-message
// sample is never held whole. It returns the capture's id and entry; a failed write removes the
// partial file and is an error, never a silent success.
func (s *mcpCaptureStore) writeCapture(lines func(w *bufio.Writer) (count, total int, err error)) (string, *mcpCapture, error) {
	keepErr := func(err error) error {
		return fmt.Errorf("could not keep the capture: %v; use a smaller max or maxBytes", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", nil, keepErr(err)
	}
	s.sweep()
	id, err := newCaptureID()
	if err != nil {
		return "", nil, keepErr(err)
	}
	file := filepath.Join(s.dir, id+".jsonl")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, keepErr(err)
	}
	bw := bufio.NewWriter(f)
	count, total, werr := lines(bw)
	if werr == nil {
		werr = bw.Flush()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(file) // a partial capture would read back as a whole one
		return "", nil, keepErr(werr)
	}
	now := time.Now()
	c := &mcpCapture{file: file, created: now, expires: now.Add(mcpCaptureTTL), count: count, total: total}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caps[id] = c
	s.evictLocked()
	return id, c, nil
}

// newCaptureID is cap- and 12 hex characters from crypto/rand.
func newCaptureID() (string, error) {
	b := make([]byte, mcpCaptureIDLen/2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "cap-" + hex.EncodeToString(b), nil
}

// evictLocked drops the expired captures, then the oldest, until the store's files total at most
// mcpCaptureStoreMax bytes. The capture just written is part of that total.
func (s *mcpCaptureStore) evictLocked() {
	now := time.Now()
	total := 0
	var live []string
	for id, c := range s.caps {
		if now.After(c.expires) {
			_ = os.Remove(c.file)
			delete(s.caps, id)
			continue
		}
		total += c.total
		live = append(live, id)
	}
	if total <= mcpCaptureStoreMax {
		return
	}
	sort.Slice(live, func(i, j int) bool { return s.caps[live[i]].created.Before(s.caps[live[j]].created) })
	for _, id := range live {
		if total <= mcpCaptureStoreMax {
			return
		}
		_ = os.Remove(s.caps[id].file)
		total -= s.caps[id].total
		delete(s.caps, id)
	}
}

// get returns the entry of id when the store still has it. A malformed id, one it does not know and
// one that expired are the same answer: the caller must take a new sample either way.
func (s *mcpCaptureStore) get(id string) (*mcpCapture, bool) {
	if !validCaptureID(id) {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.caps[id]
	if !ok {
		return nil, false
	}
	if time.Now().After(c.expires) {
		_ = os.Remove(c.file)
		delete(s.caps, id)
		return nil, false
	}
	return c, true
}

// line reads the message i of the capture.
func (c *mcpCapture) line(i int) (sampleMsg, error) {
	f, err := os.Open(c.file)
	if err != nil {
		return sampleMsg{}, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 2<<20)
	for n := 0; sc.Scan(); n++ {
		if n != i {
			continue
		}
		var m sampleMsg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return sampleMsg{}, err
		}
		return m, nil
	}
	if err := sc.Err(); err != nil {
		return sampleMsg{}, err
	}
	return sampleMsg{}, errors.New("the capture ended early")
}

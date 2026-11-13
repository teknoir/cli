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
	"strconv"
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
	// total is the payload bytes the messages hold, as a result reports them; fileBytes is what the
	// file takes on disk, which is what the store's cap counts (the two differ by the index fields
	// either side of every payload, and by a base64 payload's own overhead).
	total     int
	fileBytes int
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

// mcpStdioCaptureDir is where the stdio server keeps its captures: a directory of its own under the
// temp dir, named for the user, so that two people on one machine never read each other's captures.
// Windows has no uid (Getuid returns -1) and gives every user a temp dir of their own, so it gets
// the plain name. A variable so a test keeps its captures in its own directory.
var mcpStdioCaptureDir = func() string {
	if uid := os.Getuid(); uid >= 0 {
		return filepath.Join(os.TempDir(), "tnctl-captures-"+strconv.Itoa(uid))
	}
	return filepath.Join(os.TempDir(), "tnctl-captures")
}

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
	s.expire()
	keepErr := func(err error) error {
		return fmt.Errorf("could not keep the capture: %v; use a smaller max or maxBytes", err)
	}
	if err := ensurePrivateDir(s.dir); err != nil {
		return "", nil, err
	}
	s.sweep()
	id, err := newCaptureID()
	if err != nil {
		return "", nil, keepErr(err)
	}
	file := filepath.Join(s.dir, id+".jsonl")
	f, err := mcpCaptureOpenFile(file)
	if err != nil {
		return "", nil, keepErr(err)
	}
	bw := bufio.NewWriter(f)
	count, total, werr := lines(bw)
	if werr == nil {
		werr = bw.Flush()
	}
	fileBytes := 0
	if st, serr := f.Stat(); serr == nil {
		fileBytes = int(st.Size())
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(file) // a partial capture would read back as a whole one
		return "", nil, keepErr(werr)
	}
	now := time.Now()
	c := &mcpCapture{file: file, created: now, expires: now.Add(mcpCaptureTTL), count: count, total: total, fileBytes: fileBytes}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caps[id] = c
	s.evictLocked()
	return id, c, nil
}

// expire drops the captures that have lived their TTL. It runs before a new one is written, so that
// the room their files took is the new capture's.
func (s *mcpCaptureStore) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, c := range s.caps {
		if now.After(c.expires) {
			_ = os.Remove(c.file)
			delete(s.caps, id)
		}
	}
}

// forget drops one capture and its file: the file went from under it, and reading it again would
// only fail the same way.
func (s *mcpCaptureStore) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.caps[id]; ok {
		_ = os.Remove(c.file)
		delete(s.caps, id)
	}
}

// removeAll deletes every capture the store holds, with its file: the server that owned them has
// ended, and nobody can read a capture it does not know.
func (s *mcpCaptureStore) removeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, c := range s.caps {
		_ = os.Remove(c.file)
		delete(s.caps, id)
	}
}

// mcpCaptureOpenFile is how a capture's file is made: a new one, 0600 and never an existing file's.
// A variable so that a test can hand back a file it closed, and see the flush at the end fail.
var mcpCaptureOpenFile = func(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
// mcpCaptureStoreMax bytes. It counts the files' own bytes, which is what the disk holds: a
// payload's base64 is a third larger than the bytes it carries, and the index of every line is
// there too. The capture just written is part of that total.
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
		total += c.fileBytes
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
		total -= s.caps[id].fileBytes
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

package coder

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The session file's shape and version.
const (
	sessionVersion = 1
	// sessionFileMode and sessionDirMode are what the state file and its directories get.
	sessionFileMode = 0o600
	sessionDirMode  = 0o700
)

// sessionFile is <Home>/.local/state/teknoirbot/sessions.json.
type sessionFile struct {
	Version int                     `json:"version"`
	Threads map[string]sessionEntry `json:"threads"`
}

// sessionEntry is the OpenCode session of one council thread.
type sessionEntry struct {
	Session string `json:"session"`
	Updated int64  `json:"updated"`
}

// sessionStore is the thread to session map of one Home. A missing, unreadable, corrupt or
// unknown-version file is an empty map: it never fails a task.
type sessionStore struct {
	path    string
	mu      sync.Mutex
	threads map[string]sessionEntry
}

// loadSessions reads the thread to session map of a Home.
func loadSessions(home string) *sessionStore {
	s := &sessionStore{
		path:    filepath.Join(home, ".local", "state", "teknoirbot", "sessions.json"),
		threads: map[string]sessionEntry{},
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var f sessionFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != sessionVersion {
		return s
	}
	for thread, entry := range f.Threads {
		if !threadPattern.MatchString(thread) || !sessionPattern.MatchString(entry.Session) {
			continue
		}
		s.threads[thread] = entry
	}
	return s
}

// get is the thread's session, or "".
func (s *sessionStore) get(thread string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threads[thread].Session
}

// store records the session of a thread and writes the file.
func (s *sessionStore) store(thread, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sessionPattern.MatchString(session) {
		return nil
	}
	s.threads[thread] = sessionEntry{Session: session, Updated: time.Now().UnixMilli()}
	return s.save()
}

// save writes the map, dropping the least recently updated threads beyond MaxSessions.
//
// A state directory that is a link is refused: a link planted in one thread's home would have
// another thread's session file written through it.
func (s *sessionStore) save() error {
	if info, err := os.Lstat(filepath.Dir(s.path)); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("council coder: the state dir is not a directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.trim()
	data, err := json.Marshal(sessionFile{Version: sessionVersion, Threads: s.threads})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, sessionDirMode); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".sessions-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		// After a successful rename the file is gone; the removal is a no-op then.
		_ = os.Remove(tmp)
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, sessionFileMode); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// trim keeps the MaxSessions most recently updated threads.
func (s *sessionStore) trim() {
	for len(s.threads) > MaxSessions {
		oldest, found := "", false
		for thread, entry := range s.threads {
			if !found || entry.Updated < s.threads[oldest].Updated {
				oldest, found = thread, true
			}
		}
		delete(s.threads, oldest)
	}
}

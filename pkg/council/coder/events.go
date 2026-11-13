package coder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sync"

	"github.com/teknoir/cli/pkg/council/model"
)

// The event types of OpenCode's --format json stream that the coder uses.
const (
	eventStepStart  = "step_start"
	eventToolUse    = "tool_use"
	eventText       = "text"
	eventStepFinish = "step_finish"
	eventError      = "error"
)

// event is one line of OpenCode's JSON output.
type event struct {
	Type      string     `json:"type"`
	Timestamp int64      `json:"timestamp"`
	SessionID string     `json:"sessionID"`
	Part      *eventPart `json:"part"`
	Error     *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Status  int    `json:"status"`
	} `json:"error"`
}

// eventPart is the part an event carries.
type eventPart struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Tool  string `json:"tool"`
	State struct {
		Status string `json:"status"`
	} `json:"state"`
	Tokens struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

// state is what the event stream added up to. The reader goroutine writes it; Run reads it once
// the reader is done.
type state struct {
	mu        sync.Mutex
	sessionID string
	text      string
	steps     int
	tools     int
	usage     model.Usage
	errText   string
	haveErr   bool
}

// setSession keeps the first session id the stream carried.
func (s *state) setSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID == "" {
		s.sessionID = id
	}
}

// addTool counts a tool use.
func (s *state) addTool() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools++
}

// setText keeps the last text part: it is the report's text.
func (s *state) setText(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text = text
}

// addStep counts a finished step and adds its usage.
func (s *state) addStep(usage model.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps++
	s.usage.Add(usage)
}

// setError keeps an error event.
func (s *state) setError(kind, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.haveErr = true
	if kind != "" && message != "" {
		s.errText = kind + ": " + message
		return
	}
	s.errText = kind + message
}

// failed reports whether the stream carried an error event.
func (s *state) failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.haveErr
}

// errorText is what the error event said.
func (s *state) errorText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errText
}

// report is the run's counts, session and last text, for the Report.
func (s *state) report() (steps, tools int, usage model.Usage, sessionID, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steps, s.tools, s.usage, s.sessionID, s.text
}

// consume reads the event stream to its end, updating st and passing updates on.
func (r *Runner) consume(stdout io.Reader, task Task, st *state, budget chan<- struct{}) {
	br := bufio.NewReaderSize(stdout, 64<<10)
	halt := false
	for {
		line, tooLong, err := readLine(br)
		if !halt && !tooLong && len(line) > 0 {
			halt = r.event(line, task, st, budget)
		}
		if err != nil {
			return
		}
	}
}

// event handles one line of the stream. It reports whether the token budget is spent, which ends
// the handling of the rest of the stream.
func (r *Runner) event(line []byte, task Task, st *state, budget chan<- struct{}) bool {
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return false
	}
	if sessionPattern.MatchString(ev.SessionID) {
		st.setSession(ev.SessionID)
	}
	switch ev.Type {
	case eventStepStart:
		r.update(task, Update{Kind: KindStep})
	case eventToolUse:
		st.addTool()
		var title string
		if ev.Part != nil {
			title = ev.Part.Tool + " " + ev.Part.State.Status
		}
		r.update(task, Update{Kind: KindTool, Title: cutTotal(sanitize(title), MaxUpdateTitle)})
	case eventText:
		if ev.Part == nil {
			return false
		}
		st.setText(ev.Part.Text)
		r.update(task, Update{Kind: KindMessage, Text: cutText(sanitize(ev.Part.Text), MaxUpdateText)})
	case eventStepFinish:
		u := stepUsage(ev.Part)
		st.addStep(u)
		if task.OnStep != nil && !task.OnStep(u) {
			select {
			case budget <- struct{}{}:
			default:
			}
			return true
		}
	case eventError:
		if ev.Error == nil {
			st.setError("", "")
			return false
		}
		st.setError(ev.Error.Type, ev.Error.Message)
	}
	return false
}

// update passes one progress update on.
func (r *Runner) update(task Task, u Update) {
	if task.OnUpdate != nil {
		task.OnUpdate(u)
	}
}

// stepUsage is what one finished step cost, as the council counts usage.
func stepUsage(part *eventPart) model.Usage {
	u := model.Usage{Calls: 1}
	if part == nil {
		return u
	}
	t := part.Tokens
	u.Input = nonNegative(t.Input)
	u.Output = nonNegative(t.Output) + nonNegative(t.Reasoning)
	u.CacheRead = nonNegative(t.Cache.Read)
	u.CacheWrite = nonNegative(t.Cache.Write)
	return u
}

// nonNegative is n, or 0 when the stream reported a negative count.
func nonNegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// readLine reads one line of at most MaxEventLine bytes, without its line ending. tooLong reports a
// longer line, whose bytes are dropped: the rest of it is read and dropped too, and the stream goes
// on.
func readLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, readErr := br.ReadSlice('\n')
		if readErr == nil {
			// The line ending is not part of the line.
			chunk = bytes.TrimSuffix(chunk, []byte("\n"))
			chunk = bytes.TrimSuffix(chunk, []byte("\r"))
		}
		if !tooLong {
			if len(buf)+len(chunk) > MaxEventLine {
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch readErr {
		case nil:
			return buf, tooLong, nil
		case bufio.ErrBufferFull:
			continue
		default:
			// The last line may come without its newline; anything else ends the stream.
			if tooLong {
				return nil, true, readErr
			}
			if len(buf) > 0 {
				return buf, false, readErr
			}
			return nil, false, readErr
		}
	}
}

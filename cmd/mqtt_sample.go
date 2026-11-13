package cmd

// "tnctl mqtt sample": a finite capture of whole MQTT messages, with a summary of what was kept.
// "tnctl mqtt stream" is the endless reader that shows a binary payload as a placeholder; this one
// keeps every payload as the stream sent it and says why it stopped.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
)

// The limits of one capture: its message count, its listening time, and its payload bytes.
const (
	mqttSampleMaxCount   = 1000
	mqttSampleMaxSeconds = 120
	mqttSampleMinBytes   = 1 << 10
	mqttSampleMaxBytes   = 16 << 20
)

// mqttSampleFlags is what "tnctl mqtt sample" was asked for.
type mqttSampleFlags struct {
	count    int
	seconds  int
	maxBytes string
	out      string
	json     bool
}

// mqttSampleSummary is a capture's summary: with --json or --out one JSON line, otherwise one
// stderr line. nextMessageBytes is set only for the stop the size cap caused.
type mqttSampleSummary struct {
	Messages         int           `json:"messages"`
	Bytes            int           `json:"bytes"`
	StoppedBy        string        `json:"stoppedBy"`
	NextMessageBytes int           `json:"nextMessageBytes,omitempty"`
	ServerDropped    streamDropped `json:"serverDropped"`
}

func newMqttSampleCmd(e *cliEnv) *cobra.Command {
	var f mqttSampleFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "sample <topic>... [--count 50] [--seconds 10] [--max-bytes 16MiB] [--out FILE] [--json]",
		Short:        "Capture the MQTT messages of devices, whole, and report what was kept",
		Long: `Capture the MQTT messages of the teamspace's devices, whole and finite: use it when a message
must survive as it was sent, or when the endless output of "mqtt stream" is not what you want.
A topic is /devices/<device|+>/state/#, /devices/<device|+>/events/#, or a concrete topic below
/devices/<device|+>/state/ or /devices/<device|+>/events/; up to 8 topics are captured at once.

Each message is one line: "<topic> <payload>". A text payload is printed whole; a binary one is
printed as base64: followed by the whole base64 text, never as a placeholder. With --json each
message is one JSON line: {"i":0,"topic":...,"ts":...,"retain":false,"bytes":N,
"encoding":"utf8"|"base64","payload":"..."}, where bytes is the payload's byte count (the UTF-8
length of a text payload, the decoded length of a binary one).

The capture stops at the first of: --count messages (1 to 1000, default 50), --seconds after the
stream opened (1 to 120, default 10), the next message that would push the payloads past
--max-bytes (1KiB to 16MiB, default and maximum 16MiB; that message is not kept and its size is
reported), the end of the stream, or Ctrl-C. Opening the stream has its own limit of 20 s. There
is no reconnect. It always ends with a summary: how many messages and bytes it kept and why it
stopped. With --json or --out the summary is a JSON line {"summary":{...}}, as the last line;
otherwise it is one stderr line.

--out writes the JSON lines to FILE, created with mode 0600, and prints only the summary on
stdout. An existing file is refused unless it is empty.

The platform drops messages over 256 KiB and reports them: the summary counts them, by reason,
under serverDropped (size, rate, slow, other).`,
		Example: `  tnctl mqtt sample /devices/+/state/#
  tnctl mqtt sample /devices/cam-01/events/# --count 5 --out cam.jsonl
  tnctl mqtt sample /devices/+/events/# --seconds 60 --max-bytes 4MiB --json`,
		Args: argsRange(1, api.MaxStreamTopics, "mqtt sample needs 1 to 8 topics, for example: tnctl mqtt sample /devices/+/state/#"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.count < 1 || f.count > mqttSampleMaxCount {
				return usageErr("--count must be from 1 to %d", mqttSampleMaxCount)
			}
			if f.seconds < 1 || f.seconds > mqttSampleMaxSeconds {
				return usageErr("--seconds must be from 1 to %d", mqttSampleMaxSeconds)
			}
			maxBytes, err := parseByteSize(f.maxBytes)
			if err != nil {
				return usageErr("%v", err)
			}
			if err := api.ValidateStreamTopics(args); err != nil {
				return usageErr("%v", err)
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			w, err := newMqttSampleWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
			if err != nil {
				return err
			}
			ctx, stop := signalContext(cmd.Context())
			defer stop()
			run := &mqttSampleRun{max: f.count, maxBytes: maxBytes, stoppedBy: "end", w: w}
			if err := run.run(ctx, fl, args, f.seconds); err != nil {
				_ = w.close() // whatever was written stays; the error is the answer
				return err
			}
			return w.summary(run)
		},
	}
	c.Flags().IntVar(&f.count, "count", 50, "stop after this many messages (1 to 1000)")
	c.Flags().IntVar(&f.seconds, "seconds", 10, "stop after this many seconds once the stream opened (1 to 120)")
	c.Flags().StringVar(&f.maxBytes, "max-bytes", "16MiB", "stop before the payloads pass this many bytes: a byte count, or KiB or MiB (1KiB to 16MiB)")
	c.Flags().StringVar(&f.out, "out", "", "write the JSON lines to this file (mode 0600) and print only the summary")
	c.Flags().BoolVar(&f.json, "json", false, "print each message as one JSON line")
	return c
}

// parseByteSize reads --max-bytes: a plain byte count, or one with KiB, MiB, K, M, KB or MB (the
// same powers of 1024). Anything else, and a value outside 1KiB to 16MiB, is an error naming the
// range.
func parseByteSize(s string) (int, error) {
	text := strings.TrimSpace(s)
	i := 0
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	num, suffix := text[:i], strings.ToLower(strings.TrimSpace(text[i:]))
	bad := fmt.Errorf("--max-bytes %q is not a byte count: use a number, or KiB or MiB, from 1KiB to 16MiB", s)
	if num == "" {
		return 0, bad
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, bad
	}
	mult := 1
	switch suffix {
	case "":
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	default:
		return 0, bad
	}
	if n > mqttSampleMaxBytes/mult {
		return 0, fmt.Errorf("--max-bytes must be from 1KiB to 16MiB, got %q", s)
	}
	if v := n * mult; v < mqttSampleMinBytes || v > mqttSampleMaxBytes {
		return 0, fmt.Errorf("--max-bytes must be from 1KiB to 16MiB, got %q", s)
	}
	return n * mult, nil
}

// mqttSampleWriter puts the capture where the flags say: the JSON lines in --out FILE (or on
// stdout), and the summary last.
type mqttSampleWriter struct {
	out, errw io.Writer
	json      bool
	file      *bufio.Writer
	f         *os.File
}

// newMqttSampleWriter opens --out, refusing a file that already holds something: the capture is
// appended to nothing. It never truncates and never follows a link. A new file is made with O_EXCL,
// which refuses any symbolic link, dangling or not; an existing one is opened with O_NOFOLLOW (where
// the system has it), and refused unless what was opened is a regular file of size 0 with one name,
// so that a link planted at the path cannot make tnctl write into something of someone else's.
func newMqttSampleWriter(out, errw io.Writer, f mqttSampleFlags) (*mqttSampleWriter, error) {
	w := &mqttSampleWriter{out: out, errw: errw, json: f.json}
	if f.out == "" {
		return w, nil
	}
	file, err := os.OpenFile(f.out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		file, err = os.OpenFile(f.out, os.O_WRONLY|mqttNoFollow, 0o600)
		if err != nil {
			return nil, newErr(codeError, "%s exists and cannot be used (a symbolic link is refused): %v", f.out, err)
		}
	} else if err != nil {
		return nil, newErr(codeError, "%v", err)
	}
	st, err := file.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", f.out)
	}
	if err == nil {
		err = mqttSingleLink(st, f.out)
	}
	if err == nil && st.Size() > 0 {
		err = fmt.Errorf("%s exists and is not empty: give a new file, or empty this one", f.out)
	}
	if err == nil {
		err = file.Chmod(0o600)
	}
	if err != nil {
		_ = file.Close()
		return nil, usageErr("%v", err)
	}
	w.f, w.file = file, bufio.NewWriter(file)
	return w, nil
}

// message writes one message as it arrived, so a capture is never held in memory.
func (w *mqttSampleWriter) message(line sampleMsg) error {
	if w.file != nil || w.json {
		b, err := mcpMarshal(line)
		if err != nil {
			return newErr(codeError, "cannot write the capture: %v", err)
		}
		b = append(b, '\n')
		if w.file != nil {
			_, err = w.file.Write(b)
		} else {
			_, err = w.out.Write(b)
		}
		if err != nil {
			return newErr(codeError, "cannot write the capture: %v", err)
		}
		return nil
	}
	payload := line.Payload
	if line.Encoding == "base64" {
		payload = "base64:" + payload
	}
	if _, err := fmt.Fprintf(w.out, "%s %s\n", escapeLine(line.Topic), escapeLine(payload)); err != nil {
		return newErr(codeError, "cannot write the capture: %v", err)
	}
	return nil
}

// summary ends the capture: the JSON summary is the last line of the file and of stdout, and the
// plain one is a single stderr line.
func (w *mqttSampleWriter) summary(run *mqttSampleRun) error {
	s := mqttSampleSummary{
		Messages: run.messages, Bytes: run.bytes, StoppedBy: run.stoppedBy,
		NextMessageBytes: run.nextMessageBytes, ServerDropped: run.dropped,
	}
	if w.file != nil || w.json {
		b, err := mcpMarshal(map[string]mqttSampleSummary{"summary": s})
		if err != nil {
			return newErr(codeError, "cannot write the summary: %v", err)
		}
		if w.file != nil {
			if _, err := w.file.Write(append(b, '\n')); err != nil {
				return newErr(codeError, "cannot write the summary: %v", err)
			}
			if err := w.close(); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w.out, "%s\n", b); err != nil {
			return newErr(codeError, "cannot write the summary: %v", err)
		}
		return nil
	}
	if _, err := fmt.Fprintf(w.errw, "tnctl: %s\n", sampleSummaryLine(s)); err != nil {
		return newErr(codeError, "cannot write the summary: %v", err)
	}
	return nil
}

// close flushes and closes --out, reporting a write that failed. It runs at most once.
func (w *mqttSampleWriter) close() error {
	if w.f == nil {
		return nil
	}
	file, bw := w.f, w.file
	w.f, w.file = nil, nil
	err := bw.Flush()
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return newErr(codeError, "cannot write %s: %v", file.Name(), err)
	}
	return nil
}

// sampleSummaryLine is the plain summary of a capture, without "tnctl: ": what it kept, what
// stopped it, and what the platform dropped.
func sampleSummaryLine(s mqttSampleSummary) string {
	line := fmt.Sprintf("%d messages, %d bytes, stopped by %s", s.Messages, s.Bytes, s.StoppedBy)
	if s.StoppedBy == "size" && s.NextMessageBytes > 0 {
		// The message it refused is what says how near the cap it came.
		line += fmt.Sprintf(" (the next message has %d bytes)", s.NextMessageBytes)
	}
	if n := s.ServerDropped.total(); n > 0 {
		line += fmt.Sprintf("; the platform dropped %d (%s)", n, sampleDroppedReasons(s.ServerDropped))
	}
	if s.ServerDropped.Size > 0 {
		line += "; messages over 256 KiB are dropped by the platform"
	}
	return line
}

// sampleDroppedReasons names the dropped messages' reasons, in the order of the summary's fields.
func sampleDroppedReasons(d streamDropped) string {
	var parts []string
	for _, p := range []struct {
		name string
		n    int
	}{{"size", d.Size}, {"rate", d.Rate}, {"slow", d.Slow}, {"other", d.Other}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", p.name, p.n))
		}
	}
	return strings.Join(parts, ", ")
}

// mqttSampleRun is one capture: what it kept and why it stopped.
type mqttSampleRun struct {
	w                *mqttSampleWriter
	max, maxBytes    int
	messages, bytes  int
	dropped          streamDropped
	stoppedBy        string
	nextMessageBytes int
}

// run opens the stream and captures until one of the limits ends it. A platform error is the
// command's error; Ctrl-C is a clean stop that still reports the summary.
func (r *mqttSampleRun) run(ctx context.Context, fl *api.Fleet, topics []string, seconds int) error {
	out, err := mqttCollect(ctx, fl, mqttCollectOpts{
		topics: topics, seconds: seconds, unit: time.Second, openMax: mcpSampleOpenMax,
		openCode: codeTimeout, interrupt: "interrupted",
	}, r.keep)
	if err != nil {
		return err
	}
	r.stoppedBy, r.nextMessageBytes, r.dropped = out.stoppedBy, out.nextMessageBytes, out.dropped
	return nil
}

// keep writes one message unless it would pass --max-bytes, and stops at --count.
func (r *mqttSampleRun) keep(m *api.StreamMessage, _ string) (string, int, error) {
	line, _ := sampleMsgOf(r.messages, m)
	if r.bytes+line.Bytes > r.maxBytes {
		// The message is not kept: the capture would pass --max-bytes with it.
		return "size", line.Bytes, nil
	}
	if err := r.w.message(line); err != nil {
		return "", 0, err
	}
	r.messages++
	r.bytes += line.Bytes
	if r.messages >= r.max {
		return "count", 0, nil
	}
	return "", 0, nil
}

// mqttCollectOpts is what one capture asks of the stream.
type mqttCollectOpts struct {
	topics    []string
	seconds   int
	unit      time.Duration // what one second of the listening time is; a test shortens it
	openMax   time.Duration // how long the platform has to open the stream
	openCode  int           // the exit code when it does not open it in time
	interrupt string        // the stoppedBy of a context that ends; "" returns the context's error instead
}

// mqttCollectOutcome is what one capture saw.
type mqttCollectOutcome struct {
	stoppedBy        string
	nextMessageBytes int
	dropped          streamDropped
}

// keepFunc handles one message of a capture: stop ends the run with that reason (keep decides
// whether the message was kept), and nextMessageBytes reports a message it refused for its size.
type keepFunc func(m *api.StreamMessage, data string) (stop string, nextMessageBytes int, err error)

// mqttCollect opens the stream of topics, opens it with its own bound, listens for seconds, and
// hands every message to keep until keep ends the run, the listening time is up, the stream ends,
// the context ends or the platform reports an error. There is no reconnect, and a stream the
// platform does not open in time is an error, never an empty success: that would read as "the
// device published nothing".
func mqttCollect(ctx context.Context, fl *api.Fleet, o mqttCollectOpts, keep keepFunc) (mqttCollectOutcome, error) {
	var out mqttCollectOutcome
	// The key is read inside the open bound, by MqttStream itself: read here first, a token answer
	// that blocks would keep Ctrl-C from reaching the interrupted path below.
	// Opening the stream has a bound of its own: the platform reads the broker login, connects and
	// subscribes before it answers, and none of that is listening time.
	octx, ocancel := context.WithCancelCause(ctx)
	defer ocancel(nil)
	open := time.AfterFunc(o.openMax, func() { ocancel(errSampleOpen) })
	sub, err := fl.MqttStream(octx, o.topics)
	inTime := open.Stop()
	if err != nil || !inTime {
		if err == nil {
			_ = sub.Close() // the bound fired as the stream opened: its cancel ends the stream
		}
		switch {
		case ctx.Err() != nil:
			if o.interrupt != "" {
				out.stoppedBy = o.interrupt
				return out, nil
			}
			return out, ctx.Err()
		case !inTime || errors.Is(context.Cause(octx), errSampleOpen):
			return out, newErr(o.openCode, "the platform did not open the MQTT stream within %s; its broker may be unreachable", o.openMax)
		}
		return out, classify(err)
	}
	defer func() { _ = sub.Close() }()
	// The listening time starts now, with the stream open. Its cancel ends a blocked Next() (it
	// carries the request), even when only ": keepalive" lines arrive.
	listen := time.AfterFunc(time.Duration(o.seconds)*o.unit, func() { ocancel(errSampleTime) })
	defer listen.Stop()
	out.stoppedBy = "end"
	for {
		ev, rerr := sub.Next()
		if rerr != nil {
			switch {
			case ctx.Err() != nil:
				if o.interrupt != "" {
					out.stoppedBy = o.interrupt
					return out, nil
				}
				return out, ctx.Err()
			case errors.Is(context.Cause(octx), errSampleTime):
				out.stoppedBy = "seconds" // classify would call it a timeout
			case errors.Is(rerr, io.EOF):
				out.stoppedBy = "end"
			default:
				return out, classify(rerr)
			}
			return out, nil
		}
		switch ev.Name {
		case api.StreamEventMessage:
			m, perr := api.ParseStreamMessage(ev.Data)
			if perr != nil {
				return out, perr
			}
			stop, next, kerr := keep(m, ev.Data)
			if kerr != nil {
				return out, kerr
			}
			if stop != "" {
				out.stoppedBy, out.nextMessageBytes = stop, next
				return out, nil
			}
		case api.StreamEventDropped:
			if d, derr := api.ParseStreamDropped(ev.Data); derr == nil {
				out.dropped.add(d.Reason, d.Count) // whatever the reason, rate, size, slow or a new one
			}
		case api.StreamEventError:
			se, perr := api.ParseStreamError(ev.Data)
			if perr != nil {
				return out, perr
			}
			return out, streamEndedError(se)
		}
	}
}

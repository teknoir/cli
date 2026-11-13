package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/auth"
)

// mqttReconnectDelay and mqttReconnectMax bound the pause between two connects of --reconnect: it
// starts at 1 s and doubles to 30 s. A 503 the platform marks as temporary (Retry-After) replaces
// that pause, at most mqttRetryAfterMax; mqttRetryAfterUnit is the seam that makes those tests fast.
var (
	mqttReconnectDelay = time.Second
	mqttReconnectMax   = 30 * time.Second
	mqttRetryAfterMax  = 60 * time.Second
	mqttRetryAfterUnit = time.Second
)

type mqttFlags struct {
	count     int
	seconds   int
	json      bool
	reconnect bool
}

func newMqttCmd(e *cliEnv) *cobra.Command {
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "mqtt <stream|sample> ...",
		Short:        "Follow the MQTT messages of the teamspace's devices",
	}
	c.AddCommand(newMqttStreamCmd(e), newMqttSampleCmd(e))
	return c
}

// streamDropped counts the messages the platform dropped, by the reason it gave: a dropped event of
// the stream, or a tool result's serverDropped. An unknown or empty reason counts as other.
type streamDropped struct {
	Size  int `json:"size"`
	Rate  int `json:"rate"`
	Slow  int `json:"slow"`
	Other int `json:"other"`
}

// add counts n messages the platform dropped for reason.
func (d *streamDropped) add(reason string, n int) {
	switch reason {
	case "size":
		d.Size += n
	case "rate":
		d.Rate += n
	case "slow":
		d.Slow += n
	default:
		d.Other += n
	}
}

// total is every dropped message, whatever the reason.
func (d streamDropped) total() int { return d.Size + d.Rate + d.Slow + d.Other }

// sampleMsg is one message of a sample as a JSON line: the CLI's --json output, and a line of a
// capture (which also carries the sha256 of the payload's bytes).
type sampleMsg struct {
	I        int    `json:"i"`
	Topic    string `json:"topic"`
	TS       string `json:"ts"`
	Retain   bool   `json:"retain"`
	Bytes    int    `json:"bytes"`
	Encoding string `json:"encoding"`
	SHA256   string `json:"sha256,omitempty"`
	Payload  string `json:"payload"`
}

// sampleMsgOf is m's line for i: the payload as the stream sent it (text, or base64 for a binary
// one) and its decoded bytes (the text, or what the base64 holds), which the byte count and the
// hash are taken from.
func sampleMsgOf(i int, m *api.StreamMessage) (sampleMsg, []byte) {
	line := sampleMsg{I: i, Topic: m.Topic, TS: m.TS, Retain: m.Retain, Encoding: "utf8", Payload: m.Payload}
	decoded := []byte(m.Payload)
	if m.Payload == "" && m.PayloadBase64 != "" {
		line.Encoding, line.Payload = "base64", m.PayloadBase64
		b, err := base64.StdEncoding.DecodeString(m.PayloadBase64)
		if err != nil {
			// Not base64 tnctl can decode: count what such text would hold.
			b = make([]byte, base64.StdEncoding.DecodedLen(len(m.PayloadBase64)))
		}
		decoded = b
	}
	line.Bytes = len(decoded)
	return line, decoded
}

func newMqttStreamCmd(e *cliEnv) *cobra.Command {
	var f mqttFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "stream <topic>... [--count N] [--seconds S] [--json] [--reconnect]",
		Short:        "Print the MQTT messages of devices as they arrive",
		Long: `Follow MQTT topics of the teamspace's devices and print their messages as they arrive.

A topic is /devices/<device|+>/state/#, /devices/<device|+>/events/#, or a concrete topic below
/devices/<device|+>/state/ or /devices/<device|+>/events/. Up to 8 topics are followed at once.

Each message is one line: "<topic> <payload>"; with --json, each message's data is one JSON line.
A message that is not text shows as <base64:N bytes>. When the platform had to drop messages it
says so on stderr; when it reports an error the command prints it and exits non-zero.

--count stops after N messages and --seconds after S seconds (exit 0 once a stream opened); Ctrl-C
stops cleanly as well. --reconnect connects again after the stream ends, the network fails, or the
gateway answers 502, 503 or 504 while the portal restarts, waiting 1 s and doubling to 30 s, and
waits out a 503 with Retry-After for that long (at most 60 s), and a 429 that says the bot's request
budget (more than 120 requests in a minute) is spent; it does not reconnect after an error the
platform reports, a refused request (400, 401, 403, 404, a 429 about too many live streams or refused
bot keys, 500) or "live MQTT is not configured". If no stream opened before --seconds ends, it exits
with the last answer's code, or 4 (timeout) when no answer came.`,
		Example: `  tnctl mqtt stream /devices/+/state/#
  tnctl mqtt stream /devices/vision-01/events/# --count 5 --json
  tnctl mqtt stream /devices/+/state/# --seconds 60 --reconnect`,
		Args: argsRange(1, api.MaxStreamTopics, "mqtt stream needs 1 to 8 topics, for example: tnctl mqtt stream /devices/+/state/#"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.count < 0 {
				return usageErr("--count must be 0 or more")
			}
			if f.seconds < 0 {
				return usageErr("--seconds must be 0 or more")
			}
			if err := api.ValidateStreamTopics(args); err != nil {
				return usageErr("%v", err)
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			ctx, stop := signalContext(ctx)
			defer stop()
			if f.seconds > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(f.seconds)*time.Second)
				defer cancel()
			}
			s := &mqttPrinter{out: cmd.OutOrStdout(), errw: cmd.ErrOrStderr(), json: f.json}
			return runMqttStream(ctx, fl, args, f, s)
		},
	}
	c.Flags().IntVar(&f.count, "count", 0, "stop after this many messages (0: never)")
	c.Flags().IntVar(&f.seconds, "seconds", 0, "stop after this many seconds (0: never)")
	c.Flags().BoolVar(&f.json, "json", false, "print each message's data as one JSON line")
	c.Flags().BoolVar(&f.reconnect, "reconnect", false, "connect again when the stream ends, the network fails, the portal restarts (502, 503, 504) or a bot's request budget (more than 120 requests in a minute) says wait (429); a --seconds run that never connected then fails")
	return c
}

// escapeLine keeps one message on one terminal line and drives nothing: \r and \n show escaped,
// any other control character but tab (ESC, BEL, NUL, DEL, C1 such as U+009B) as \u00XX.
func escapeLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mqttPrinter writes what the stream delivers and counts it.
type mqttPrinter struct {
	out, errw   io.Writer
	json        bool
	messages    int
	events      int
	lines       int
	lastErrNote string
}

// message prints one message event from the event's data as the server sent it.
func (p *mqttPrinter) message(m *api.StreamMessage, data string) error {
	p.messages++
	p.events++
	if p.json {
		// The server's data as written (one line), so a field tnctl does not know survives.
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(data)); err != nil {
			return fmt.Errorf("unexpected message from the MQTT stream: %w", err)
		}
		_, err := fmt.Fprintf(p.out, "%s\n", b.Bytes())
		return err
	}
	payload := m.Payload
	if payload == "" && m.PayloadBase64 != "" {
		n := len(m.PayloadBase64)
		if b, err := base64.StdEncoding.DecodeString(m.PayloadBase64); err == nil {
			n = len(b)
		}
		payload = fmt.Sprintf("<base64:%d bytes>", n)
	}
	_, err := fmt.Fprintf(p.out, "%s %s\n", escapeLine(m.Topic), escapeLine(payload))
	return err
}

// dropped reports messages the platform had to drop.
func (p *mqttPrinter) dropped(d *api.StreamDropped) {
	p.events++
	reason := terminalText(d.Reason)
	switch reason {
	case "rate":
		reason = "too many messages too fast"
	case "size":
		reason = "the messages were too large"
	case "slow":
		reason = "the reader was too slow"
	case "":
		reason = "unknown reason"
	}
	fmt.Fprintf(p.errw, "tnctl: the platform dropped %d message(s): %s\n", d.Count, reason)
}

// streamEndedError ends a stream the platform ended with an error event.
func streamEndedError(e *api.StreamError) error {
	msg := terminalText(e.Message)
	if msg == "" {
		msg = "the platform ended the stream"
	}
	return newErr(codeError, "%s", msg)
}

// runMqttStream follows the topics until --count, --seconds or Ctrl-C stops it. With --reconnect it
// connects again after the stream ended, the network failed, or the gateway answered 502, 503 or 504
// while the portal's pod is being replaced: never after a refused request (400, 401, 403, 404, 500),
// a 429 about too many live streams or refused bot keys, an error event, or a 503 that says live MQTT
// is not configured. A 503 that the platform marks as temporary, or a 429 that says the bot's request
// budget is spent (Retry-After), waits that long instead of the backoff. Without --reconnect only a
// clean end of the stream, --count or --seconds after a stream opened ends the command with 0; a
// network failure is an error. --seconds that runs out before any stream opened is an error too: its
// exit code and answer are the last failed round's (a timeout when the platform had not answered at
// all), since no messages is otherwise indistinguishable from "the stream was up and quiet".
func runMqttStream(ctx context.Context, fl *api.Fleet, topics []string, f mqttFlags, p *mqttPrinter) error {
	// The key is read once before the loop: a missing key answers at once, with or without
	// --reconnect, and no request is sent.
	if _, err := fl.Token(ctx); err != nil {
		return classify(err)
	}
	delay := mqttReconnectDelay
	opened := false   // some round of this run got a stream
	var lastErr error // the answer of the latest round that failed
	// stop ends a run its context ended: --seconds without a stream in the window is a failure,
	// Ctrl-C is a clean stop.
	stop := func() error {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) || opened {
			return nil // Ctrl-C, or --seconds after a stream opened
		}
		if lastErr != nil {
			return newErr(exitCodeOf(lastErr), "no stream opened in %ds; last answer: %w", f.seconds, lastErr)
		}
		// The first connect still waited for its answer: a platform that cannot reach its broker
		// answers only after its 10 s connect timeout.
		return newErr(codeTimeout, "no stream opened in %ds: the platform did not answer in time (its MQTT broker may be unreachable)", f.seconds)
	}
	for {
		before := p.events
		done, retry, openedOnce, wait, err := runMqttStreamOnce(ctx, fl, topics, f, p)
		if openedOnce {
			opened = true
		}
		if err != nil {
			lastErr = err
		}
		if done {
			if err != nil {
				return err
			}
			return stop()
		}
		if ctx.Err() != nil {
			return stop()
		}
		if !f.reconnect || !retry {
			return err
		}
		if wait > 0 {
			// The platform said how long to wait (a portal restart, a broker it cannot reach,
			// a bot's request budget): use that instead of the backoff, and leave the backoff
			// as it is.
			if wait > mqttRetryAfterMax {
				wait = mqttRetryAfterMax
			}
			msg := "the platform asked the stream to wait"
			var ae *api.Error
			if errors.As(err, &ae) {
				msg = retryReason(ae)
			}
			_, _ = fmt.Fprintf(p.errw, "tnctl: %s; connecting again in %s\n", msg, wait)
			if err := realSleep(ctx, wait); err != nil {
				return stop() // stopped while waiting to reconnect
			}
			continue
		}
		if p.events > before {
			// The connection worked: the next wait starts at the first delay again.
			delay = mqttReconnectDelay
		}
		// The gateway's own answer while the portal's pod is being replaced (a 503 that never
		// reached it, a 502 or 504 for one that did) is the reason, not a silent end.
		msg := "the stream ended"
		var ae *api.Error
		if errors.As(err, &ae) {
			msg = retryReason(ae)
		} else if err != nil {
			// The cause, without the stream URL: it repeats every topic and would push the cause
			// past the 300 characters of the line.
			cause := err
			var ue *url.Error
			if errors.As(err, &ue) {
				cause = ue.Err
			}
			msg = "the stream failed: " + retryReason(&api.Error{Message: cause.Error()})
		}
		_, _ = fmt.Fprintf(p.errw, "tnctl: %s; connecting again in %s\n", msg, delay)
		if err := realSleep(ctx, delay); err != nil {
			return stop() // stopped while waiting to reconnect
		}
		delay *= 2
		if delay > mqttReconnectMax {
			delay = mqttReconnectMax
		}
	}
}

// retryReason is the answer's message for the "connecting again" line: one line (a gateway's text
// body may have several), at most 300 characters, or "HTTP <status>" when it has none.
func retryReason(ae *api.Error) string {
	msg := terminalText(ae.Message)
	if msg == "" {
		return fmt.Sprintf("HTTP %d", ae.Status)
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "..."
	}
	return msg
}

// terminalText is s on one line for the terminal: whitespace becomes single spaces and other control
// characters (ESC, BEL, NUL, C1 such as U+009B) are dropped, so text from the platform never drives
// the terminal.
func terminalText(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)), " ")
}

// retryAfterWait is how long an answer with Retry-After asks --reconnect to wait: that many
// mqttRetryAfterUnit, at most mqttRetryAfterMax. It is capped in whole units before the
// multiplication: a huge Retry-After (or an HTTP date that saturates at 292 years) must not
// overflow time.Duration.
func retryAfterWait(ae *api.Error) time.Duration {
	wait := mqttRetryAfterMax
	if n := time.Duration(ae.RetryAfter); n < mqttRetryAfterMax/mqttRetryAfterUnit {
		wait = n * mqttRetryAfterUnit
	}
	return wait
}

// runMqttStreamOnce is one connection. done says the run is over (the limit was reached or a problem
// no reconnect cures: its error is returned). retry says --reconnect may connect again; err then is
// the network failure, the gateway's 502/503/504, or nil for a clean end of the stream. opened says
// the platform returned a subscription (the round got a stream). wait is a pause the platform asked
// for (a temporary 503, or a 429 that says the bot's request budget is spent; 0: use the backoff).
func runMqttStreamOnce(ctx context.Context, fl *api.Fleet, topics []string, f mqttFlags, p *mqttPrinter) (done, retry, opened bool, wait time.Duration, err error) {
	sub, err := fl.MqttStream(ctx, topics)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			if ae.Status == http.StatusServiceUnavailable &&
				strings.Contains(ae.Message, "live MQTT is not configured") {
				// Nothing in the teamspace serves live MQTT: connecting again does not help.
				return true, false, false, 0, classify(err)
			}
			if ae.Status == http.StatusServiceUnavailable && ae.RetryAfter > 0 {
				// The platform says it is temporary (a portal restart, a broker it cannot reach):
				// --reconnect waits as long as its Retry-After says and connects again.
				return false, true, false, retryAfterWait(ae), classify(err)
			}
			if ae.Status == http.StatusTooManyRequests && f.reconnect && ae.RetryAfter > 0 &&
				strings.Contains(ae.Message, "requests in a minute") {
				// A bot's request budget (more than 120 requests in a minute) is temporary and
				// says how long: --reconnect waits it out like a temporary 503. Too many live
				// streams open is not (only closing one helps), and neither is "Too many refused
				// bot keys" (a valid key never gets it: the key is bad).
				return false, true, false, retryAfterWait(ae), classify(err)
			}
			switch ae.Status {
			case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				// The gateway's own answer while the portal's pod is being replaced: it never
				// reached the platform, so --reconnect uses the backoff.
				return false, true, false, 0, classify(err)
			}
			// A refused request or a rejected key is not retried: it says nothing about the network.
			return true, false, false, 0, classify(err)
		}
		if errors.Is(err, api.ErrBotKey) || errors.Is(err, api.ErrBotAPIInsecure) ||
			errors.Is(err, auth.ErrNotLoggedIn) || errors.Is(err, auth.ErrSessionExpired) {
			// The token was read again for this connection and could not be: a missing or emptied bot
			// key, or a person's login that ended. Connecting again would not help.
			return true, false, false, 0, classify(err)
		}
		if ctx.Err() != nil {
			return true, false, false, 0, nil
		}
		return false, true, false, 0, classify(err) // a network failure: --reconnect may try again
	}
	defer sub.Close()
	for {
		ev, rerr := sub.Next()
		if rerr != nil {
			if ctx.Err() != nil {
				return true, false, true, 0, nil // --seconds ran out, or Ctrl-C
			}
			if errors.Is(rerr, io.EOF) {
				return false, true, true, 0, nil // the server ended the stream cleanly
			}
			return false, true, true, 0, classify(rerr) // a network failure, or a stream event past the limit
		}
		switch ev.Name {
		case api.StreamEventMessage:
			m, perr := api.ParseStreamMessage(ev.Data)
			if perr != nil {
				return true, false, true, 0, perr
			}
			if werr := p.message(m, ev.Data); werr != nil {
				return true, false, true, 0, werr
			}
			if f.count > 0 && p.messages >= f.count {
				return true, false, true, 0, nil
			}
		case api.StreamEventDropped:
			if d, derr := api.ParseStreamDropped(ev.Data); derr == nil {
				p.dropped(d)
			}
		case api.StreamEventError:
			e, eerr := api.ParseStreamError(ev.Data)
			if eerr != nil {
				return true, false, true, 0, eerr
			}
			return true, false, true, 0, streamEndedError(e)
		}
	}
}

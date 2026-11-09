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
	"strings"
	"time"

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
		Use:          "mqtt <stream> ...",
		Short:        "Follow the MQTT messages of the teamspace's devices",
	}
	c.AddCommand(newMqttStreamCmd(e))
	return c
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

--count stops after N messages and --seconds after S seconds (exit 0); Ctrl-C stops cleanly as
well. --reconnect connects again after the stream ends or the network fails, waiting 1 s and
doubling to 30 s, and waits out a temporary 503 for its Retry-After; it does not reconnect after
an error the platform reports or a refused request.`,
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
	c.Flags().BoolVar(&f.reconnect, "reconnect", false, "connect again when the stream ends or the network fails")
	return c
}

// mqttPayloadEscaper keeps one message on one line: a text payload shows its newlines escaped.
var mqttPayloadEscaper = strings.NewReplacer("\r", `\r`, "\n", `\n`)

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
	payload = mqttPayloadEscaper.Replace(payload)
	_, err := fmt.Fprintf(p.out, "%s %s\n", m.Topic, payload)
	return err
}

// dropped reports messages the platform had to drop.
func (p *mqttPrinter) dropped(d *api.StreamDropped) {
	p.events++
	reason := d.Reason
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
	msg := e.Message
	if msg == "" {
		msg = "the platform ended the stream"
	}
	return newErr(codeError, "%s", msg)
}

// runMqttStream follows the topics until --count, --seconds or Ctrl-C stops it. With --reconnect it
// connects again after the stream ended, the network failed, or the gateway answered 502, 503 or 504
// while the portal's pod is being replaced: never after a refused request (400, 401, 403, 404, 429,
// 500), an error event, or a 503 that says live MQTT is not configured. A 503 that the platform marks
// as temporary waits its Retry-After instead of the backoff. Without --reconnect only a clean end of
// the stream, --count or --seconds ends the command with 0; a network failure is an error.
func runMqttStream(ctx context.Context, fl *api.Fleet, topics []string, f mqttFlags, p *mqttPrinter) error {
	// The key is read once before the loop: a missing key answers at once, with or without
	// --reconnect, and no request is sent.
	if _, err := fl.Token(ctx); err != nil {
		return classify(err)
	}
	delay := mqttReconnectDelay
	for {
		before := p.events
		done, retry, wait, err := runMqttStreamOnce(ctx, fl, topics, f, p)
		if done {
			return err
		}
		if ctx.Err() != nil {
			return nil // --seconds ran out, or Ctrl-C: a clean stop
		}
		if !f.reconnect || !retry {
			return err
		}
		if wait > 0 {
			// The platform said how long to wait (a portal restart, a broker it cannot reach):
			// use that instead of the backoff, and leave the backoff as it is.
			if wait > mqttRetryAfterMax {
				wait = mqttRetryAfterMax
			}
			msg := "the platform asked the stream to wait"
			var ae *api.Error
			if errors.As(err, &ae) && ae.Message != "" {
				msg = ae.Message
			}
			_, _ = fmt.Fprintf(p.errw, "tnctl: %s; connecting again in %s\n", msg, wait)
			if err := realSleep(ctx, wait); err != nil {
				return nil // stopped while waiting to reconnect
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
			msg = ae.Message
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", ae.Status)
			}
		}
		_, _ = fmt.Fprintf(p.errw, "tnctl: %s; connecting again in %s\n", msg, delay)
		if err := realSleep(ctx, delay); err != nil {
			return nil // stopped while waiting to reconnect
		}
		delay *= 2
		if delay > mqttReconnectMax {
			delay = mqttReconnectMax
		}
	}
}

// runMqttStreamOnce is one connection. done says the run is over (the limit was reached or a problem
// no reconnect cures: its error is returned). retry says --reconnect may connect again; err then is
// the network failure, the gateway's 502/503/504, or nil for a clean end of the stream. wait is a
// pause the platform asked for (a 503 with Retry-After, 0: use the backoff).
func runMqttStreamOnce(ctx context.Context, fl *api.Fleet, topics []string, f mqttFlags, p *mqttPrinter) (done, retry bool, wait time.Duration, err error) {
	sub, err := fl.MqttStream(ctx, topics)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			if ae.Status == http.StatusServiceUnavailable &&
				strings.Contains(ae.Message, "live MQTT is not configured") {
				// Nothing in the teamspace serves live MQTT: connecting again does not help.
				return true, false, 0, classify(err)
			}
			if ae.Status == http.StatusServiceUnavailable && ae.RetryAfter > 0 {
				// The platform says it is temporary (a portal restart, a broker it cannot reach):
				// --reconnect waits as long as its Retry-After says and connects again.
				// Capped in whole units before the multiplication: a huge Retry-After (or an HTTP
				// date that saturates at 292 years) must not overflow time.Duration.
				wait = mqttRetryAfterMax
				if n := time.Duration(ae.RetryAfter); n < mqttRetryAfterMax/mqttRetryAfterUnit {
					wait = n * mqttRetryAfterUnit
				}
				return false, true, wait, classify(err)
			}
			switch ae.Status {
			case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				// The gateway's own answer while the portal's pod is being replaced: it never
				// reached the platform, so --reconnect uses the backoff.
				return false, true, 0, classify(err)
			}
			// A refused request or a rejected key is not retried: it says nothing about the network.
			return true, false, 0, classify(err)
		}
		if errors.Is(err, api.ErrBotKey) || errors.Is(err, api.ErrBotAPIInsecure) ||
			errors.Is(err, auth.ErrNotLoggedIn) || errors.Is(err, auth.ErrSessionExpired) {
			// The token was read again for this connection and could not be: a missing or emptied bot
			// key, or a person's login that ended. Connecting again would not help.
			return true, false, 0, classify(err)
		}
		if ctx.Err() != nil {
			return true, false, 0, nil
		}
		return false, true, 0, classify(err) // a network failure: --reconnect may try again
	}
	defer sub.Close()
	for {
		ev, rerr := sub.Next()
		if rerr != nil {
			if ctx.Err() != nil {
				return true, false, 0, nil // --seconds ran out, or Ctrl-C
			}
			if errors.Is(rerr, io.EOF) {
				return false, true, 0, nil // the server ended the stream cleanly
			}
			return false, true, 0, classify(rerr) // a network failure, or a stream event past the limit
		}
		switch ev.Name {
		case api.StreamEventMessage:
			m, perr := api.ParseStreamMessage(ev.Data)
			if perr != nil {
				return true, false, 0, perr
			}
			if werr := p.message(m, ev.Data); werr != nil {
				return true, false, 0, werr
			}
			if f.count > 0 && p.messages >= f.count {
				return true, false, 0, nil
			}
		case api.StreamEventDropped:
			if d, derr := api.ParseStreamDropped(ev.Data); derr == nil {
				p.dropped(d)
			}
		case api.StreamEventError:
			e, eerr := api.ParseStreamError(ev.Data)
			if eerr != nil {
				return true, false, 0, eerr
			}
			return true, false, 0, streamEndedError(e)
		}
	}
}

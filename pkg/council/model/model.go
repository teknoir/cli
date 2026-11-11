// Package model is the council's client of the bot API's Messages proxy: one streaming request to
// Claude per council turn, authenticated with the TeknoirBot's own key.
//
// The proxy allows only claude-sonnet-5-5 and claude-haiku-5-5, effort low, medium or high, and
// refuses forced tool use (tool_choice any or tool) on those two models, which is why Request has
// no field for it: a request with tools runs with tool_choice auto, or none when ToolChoiceNone asks
// for an answer.
package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/teknoir/cli/pkg/council/internal/botapi"
)

// The models the proxy allows.
const (
	ModelSonnet = "claude-sonnet-5-5"
	ModelHaiku  = "claude-haiku-5-5"
)

// MaxTokensLimit is the largest answer the council asks for.
const MaxTokensLimit = 32000

// turnHeader carries the council turn a request belongs to, so the platform's logs show it.
const turnHeader = "x-teknoir-turn"

// turnPattern is what a turn id must look like to travel in a header.
var turnPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Config is what a model client is built from.
type Config struct {
	// APIBase is the bot API's base, $TEKNOIRBOT_API (https, or http on a loopback address).
	APIBase string
	// KeyFile is the file the bot's key is read from on every request, $TNCTL_BOT_KEY_FILE.
	KeyFile string
	// Version is this tnctl's version, for the User-Agent.
	Version string
	// HTTPClient sends the requests; nil means http.DefaultClient.
	HTTPClient *http.Client
}

// Usage is what a request cost.
type Usage struct {
	Calls      int64
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// Add adds o to u.
func (u *Usage) Add(o Usage) {
	u.Calls += o.Calls
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
}

// Request is one streaming request.
type Request struct {
	// Turn is the council turn the request belongs to. It is sent as the x-teknoir-turn header when
	// it is a valid header value, and not sent otherwise.
	Turn string
	// Model is ModelSonnet or ModelHaiku.
	Model string
	// Effort is "low", "medium" or "high".
	Effort string
	// System is the system prompt, sent as one text block.
	System string
	// Messages is the conversation, oldest first.
	Messages []anthropic.MessageParam
	// Tools are the tools the model may call; empty sends none.
	Tools []anthropic.ToolUnionParam
	// ToolChoiceNone sends tool_choice {"type":"none"} when there are tools: the model must answer
	// rather than call one. Without tools it changes nothing.
	ToolChoiceNone bool
	// MaxTokens is the cap on the answer, 1 to MaxTokensLimit.
	MaxTokens int64
	// Format is an optional JSON schema the answer must follow.
	Format map[string]any
}

// Client sends streaming requests to the bot API's model proxy.
type Client struct {
	client  anthropic.Client
	keyFile string
}

// ephem is the cache control of the system block and the last tool. The TTL is set to the API's
// own default (5 minutes) because a zero-value TTL is the SDK's "not set", which would drop the
// whole cache_control object from the request.
func ephem() anthropic.CacheControlEphemeralParam {
	return anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m}
}

// New returns the model client of a TeknoirBot. It refuses a base that may not carry the bot's
// key, and an empty key file, before any request leaves.
func New(cfg Config) (*Client, error) {
	base := botapi.TrimBase(cfg.APIBase)
	if base == "" {
		return nil, fmt.Errorf("no bot API base: %w", botapi.ErrInsecure)
	}
	if !botapi.Allowed(base) {
		return nil, fmt.Errorf("%s: %w", base, botapi.ErrInsecure)
	}
	if strings.TrimSpace(cfg.KeyFile) == "" {
		return nil, errors.New("no bot key file: $TNCTL_BOT_KEY_FILE is empty")
	}
	keyFile := strings.TrimSpace(cfg.KeyFile)
	hc := noRedirect(cfg.HTTPClient)
	opts := []option.RequestOption{
		// No ANTHROPIC_* variable may take part: the key, the base and the profile all come from
		// the bot's own configuration.
		option.WithoutEnvironmentDefaults(),
		// The SDK appends /v1/messages to this base.
		option.WithBaseURL(base + "/model/"),
		option.WithHTTPClient(hc),
		option.WithMaxRetries(3),
		option.WithHeader("User-Agent", "tnctl/"+cfg.Version+" (council)"),
		option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			key, err := botapi.Key(keyFile)
			if err != nil {
				return nil, err
			}
			req.Header.Set("X-Api-Key", key)
			// The proxy reads either header; only the bot's key may travel.
			req.Header.Del("Authorization")
			resp, err := next(req)
			// The client does not follow a redirect, and the SDK turns only 4xx and 5xx into an
			// error: a 3xx would read as an empty but successful stream. It is a failed call.
			if err == nil && resp != nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
				// Not a hiccup to retry: the base is wrong, and a person has to fix it.
				resp.Header.Set("x-should-retry", "false")
				return resp, &StatusError{Status: resp.StatusCode, URL: resp.Request.URL.Redacted()}
			}
			return resp, err
		}),
	}
	return &Client{client: anthropic.NewClient(opts...), keyFile: keyFile}, nil
}

// StatusError is a non-2xx answer the SDK does not report itself: a 3xx, which the client never
// follows, because the bot's key must not travel past the base.
type StatusError struct {
	// Status is the HTTP status of the answer.
	Status int
	// URL is the address it came from, without a query string.
	URL string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("council model: HTTP %d from %s (a redirect is not followed)", e.Status, e.URL)
}

// noRedirect is a copy of httpClient (or a new one when it is nil) that does not follow redirects:
// the bot's key rides on every request, so a 3xx must never carry it to another host or to plain
// http. A 3xx is then just a non-2xx answer. The caller's client is left as it is.
func noRedirect(httpClient *http.Client) *http.Client {
	var c http.Client
	if httpClient != nil {
		c = *httpClient
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// Stream sends one streaming request. onText, if not nil, gets each text delta of the answer in
// order. It returns the accumulated message and what the request cost (Calls = 1).
func (c *Client) Stream(ctx context.Context, req Request, onText func(string)) (*anthropic.Message, Usage, error) {
	params, err := params(req)
	if err != nil {
		return nil, Usage{}, err
	}
	// A missing or empty key file is permanent: it is reported here, before the SDK would treat it
	// as a connection error and spend its retries on it. The middleware reads the file again on
	// every attempt, so a key replaced while a request runs is picked up.
	if _, err := botapi.Key(c.keyFile); err != nil {
		return nil, Usage{}, err
	}
	var opts []option.RequestOption
	if turnPattern.MatchString(req.Turn) {
		opts = append(opts, option.WithHeader(turnHeader, req.Turn))
	}

	stream := c.client.Messages.NewStreaming(ctx, params, opts...)
	defer func() { _ = stream.Close() }()

	msg := anthropic.Message{}
	// A stream that fails after its first event was still charged by the proxy: what
	// message_start said it read is reported with the error, so the turn's budget sees it.
	events := 0
	for stream.Next() {
		event := stream.Current()
		events++
		if onText != nil && event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			onText(event.Delta.Text)
		}
		if err := msg.Accumulate(event); err != nil {
			err = fmt.Errorf("council model: unexpected stream event: %w", err)
			if events > 0 {
				return nil, usage(&msg), err
			}
			return nil, Usage{}, err
		}
	}
	if err := stream.Err(); err != nil {
		if events > 0 {
			return nil, usage(&msg), err
		}
		return nil, Usage{}, err
	}
	return &msg, usage(&msg), nil
}

// params validates req and turns it into the request the proxy gets. No request is sent for a
// request that fails here.
func params(req Request) (anthropic.MessageNewParams, error) {
	var p anthropic.MessageNewParams
	switch req.Model {
	case ModelSonnet, ModelHaiku:
	default:
		return p, fmt.Errorf("council model: %q is not a model the council may use (use %s or %s)", req.Model, ModelSonnet, ModelHaiku)
	}
	switch req.Effort {
	case "low", "medium", "high":
	default:
		return p, fmt.Errorf("council model: effort %q is not one of low, medium, high", req.Effort)
	}
	if req.MaxTokens < 1 || req.MaxTokens > MaxTokensLimit {
		return p, fmt.Errorf("council model: max tokens %d is not between 1 and %d", req.MaxTokens, MaxTokensLimit)
	}
	if len(req.Messages) == 0 {
		return p, errors.New("council model: no messages")
	}

	p = anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxTokens,
		Messages:  req.Messages,
		Thinking:  anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffort(req.Effort),
		},
		// The conversation tail is the third cache breakpoint, beside the system block and the last
		// tool: the rounds of a tool loop then read it back instead of resending it.
		CacheControl: ephem(),
	}
	if req.System != "" {
		// The system prompt is the stable head of every request of a member, so it is the cache
		// breakpoint.
		p.System = []anthropic.TextBlockParam{{Text: req.System, CacheControl: ephem()}}
	}
	if req.Format != nil {
		p.OutputConfig.Format = anthropic.JSONOutputFormatParam{Schema: req.Format}
	}
	if len(req.Tools) > 0 {
		// A copy: the caller's tools are its own, and the cache breakpoint below changes one.
		p.Tools = make([]anthropic.ToolUnionParam, len(req.Tools))
		copy(p.Tools, req.Tools)
		// The tool list is the same on every request of a member, so its last tool is the second
		// cache breakpoint.
		if last := len(p.Tools) - 1; p.Tools[last].OfTool != nil {
			tool := *p.Tools[last].OfTool
			tool.CacheControl = ephem()
			p.Tools[last].OfTool = &tool
		}
		// Never "any" or "tool": the two models the proxy allows reject forced tool use.
		if req.ToolChoiceNone {
			p.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
		} else {
			p.ToolChoice = anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
		}
	}
	return p, nil
}

// usage is what the finished message cost.
func usage(msg *anthropic.Message) Usage {
	return Usage{
		Calls:      1,
		Input:      msg.Usage.InputTokens,
		Output:     msg.Usage.OutputTokens,
		CacheRead:  msg.Usage.CacheReadInputTokens,
		CacheWrite: msg.Usage.CacheCreationInputTokens,
	}
}

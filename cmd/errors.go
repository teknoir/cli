package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/auth"
)

// Exit codes of the commands that agents drive (robots, config, exec, logs). They are part of
// the interface: scripts branch on them. Listed in `tnctl config --help`.
const (
	codeOK        = 0
	codeError     = 1 // anything else
	codeUsage     = 2 // wrong arguments or flags, ambiguous resource name
	codeNotFound  = 3 // robot, device or resource does not exist
	codeTimeout   = 4 // --wait or the request timed out
	codeRejected  = 5 // the platform refused the change: validation, invalid, no-target
	codeAuth      = 6 // not logged in, login expired (JSON code "session_expired"), token rejected, or not allowed
	codeUnhealthy = 7 // applied, but the workload fails (Job failed, CrashLoopBackOff, ImagePullBackOff)
	codeConflict  = 8 // someone else changed the same thing; try again
	codeRateLimit = 9 // too many requests: wait the Retry-After time and run it again
)

var codeNames = map[int]string{
	codeError:     "error",
	codeUsage:     "usage",
	codeNotFound:  "not_found",
	codeTimeout:   "timeout",
	codeRejected:  "rejected",
	codeAuth:      "auth",
	codeUnhealthy: "unhealthy",
	codeConflict:  "conflict",
	codeRateLimit: "rate_limited",
}

// codeSessionExpiredName is the JSON error code of an expired login (exit code codeAuth).
const codeSessionExpiredName = "session_expired"

// cliError is an error with an exit code.
type cliError struct {
	code int
	err  error
}

func (e *cliError) Error() string { return e.err.Error() }
func (e *cliError) Unwrap() error { return e.err }

func newErr(code int, format string, args ...any) *cliError {
	return &cliError{code: code, err: fmt.Errorf(format, args...)}
}

func usageErr(format string, args ...any) *cliError { return newErr(codeUsage, format, args...) }

// classify gives err an exit code: API statuses, a missing login and timeouts.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var ce *cliError
	if errors.As(err, &ce) {
		return err
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return err
	}
	if errors.Is(err, auth.ErrSessionExpired) {
		// the refresh token is dead: same exit code as "not logged in", the action is the same
		return &cliError{code: codeAuth, err: &auth.SessionExpiredError{Cause: err}}
	}
	if errors.Is(err, auth.ErrNotLoggedIn) {
		return &cliError{code: codeAuth, err: errors.New("not logged in: run tnctl login")}
	}
	var up *api.UnsupportedPlatformError
	if errors.As(err, &up) {
		// not a mistake in the arguments, but the exit code scripts expect for "this cannot work as asked"
		return &cliError{code: codeUsage, err: err}
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		code := codeError
		switch {
		case ae.IsAuth():
			code = codeAuth
		case ae.Status == http.StatusNotFound:
			code = codeNotFound
		case ae.Status == http.StatusConflict:
			code = codeConflict
		case ae.Status == http.StatusTooManyRequests:
			code = codeRateLimit
		case ae.Status == http.StatusBadRequest, ae.Status == http.StatusUnprocessableEntity:
			code = codeRejected
		case ae.Status == http.StatusRequestTimeout, ae.Status == http.StatusGatewayTimeout:
			code = codeTimeout
		}
		return &cliError{code: code, err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &cliError{code: codeError, err: errors.New("interrupted")}
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return &cliError{code: codeTimeout, err: fmt.Errorf("the request timed out: %w", err)}
	}
	return err
}

// exitCodeOf is the process exit code for an error returned by a command.
func exitCodeOf(err error) int {
	if err == nil {
		return codeOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ce *cliError
	if errors.As(classify(err), &ce) {
		return ce.code
	}
	return codeError
}

// writeError prints err as `Error: message`, or as {"error":{"code","message"}} for -o json.
func writeError(w io.Writer, err error, format string) {
	code := exitCodeOf(err)
	expired := errors.Is(err, auth.ErrSessionExpired)
	if expired {
		err = classify(err) // the fixed "run tnctl login" message, not the token endpoint's text
	}
	if format == "json" {
		name := codeNames[code]
		if name == "" {
			name = "error"
		}
		if expired {
			// same exit code as "auth", its own name: agents can tell "log in again" from "not allowed"
			name = codeSessionExpiredName
		}
		body := map[string]any{"code": name, "message": err.Error()}
		var ae *api.Error
		if errors.As(err, &ae) && ae.RetryAfter > 0 {
			body["retryAfter"] = ae.RetryAfter
		}
		b, _ := json.Marshal(map[string]any{"error": body})
		fmt.Fprintln(w, string(b))
		return
	}
	fmt.Fprintln(w, "Error:", err)
}

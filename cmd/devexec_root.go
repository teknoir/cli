package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// This file is how tnctl runs a command as root on a device. The login user reaches root through
// sudo, and on most devices sudo asks for the user's password (the bootstrap gives passwordless
// sudo only to a login without a password). The platform holds that password next to the login
// key, so tnctl hands it to sudo itself, and only when sudo asks for it:
//
//  1. The command first runs with `sudo -n` and nothing else (mode rootTry). A device whose sudo
//     needs no password for it (a NOPASSWD rule, a login sudo still remembers) answers there, and
//     the password never leaves tnctl.
//  2. Only when sudo says that a password is required does the session send it (mode
//     rootWithPassword): as the first stdin line, which the wrapper reads itself, checks with
//     PAM's helper and gives to `sudo -v` once. The session remembers that sudo asks, so the next
//     command goes straight here.
//
// The same wrapper serves both modes. Whether the script started is never guessed from its
// output or exit status: the root shell says so itself with a marker line on stderr before it
// runs the script (rootStub). Before that line everything on stderr is held back, after it
// everything is the script's. The payload on stdin is not sent before that line either
// (feedStdin), so no sudo but the one that runs the script can read it, and a command that sudo
// refused has taken nothing of it.
//
// What is held back is not all sudo's: ssh and its ProxyCommand write there too (a refused
// connection, "Your tnctl login has expired"), and so does the login shell. So the wrapper also
// says where sudo speaks: a marker line before each sudo whose words matter ("ask" before
// `sudo -v`, "sudo" before the sudo of the script). sudo's reason is read from the lines after
// those markers and from nowhere else (rootStderr.sudo, sudoSaid), and never when ssh itself
// failed (exit status 255 without the script having started: rootVerdict).
//
// What step 1 costs on the device, measured with test/sudo-root: nothing, except with sudo 1.9.9
// (Ubuntu 22.04), which takes a `sudo -n` that needs a password to PAM as a failed login. Where
// such a device also has pam_faillock, every tnctl run that sudo does not end up accepting a
// password from is one failed login, and 3 lock the login (docs/commands/exec.md, "Failed
// logins"). The session asks that way once, however many root commands it runs.

// Exit statuses of the wrapper when sudo cannot be used. They are only read together with the
// marker line the wrapper prints on stderr, and only when the script never started, so a command
// that happens to exit with one of them keeps its own exit status.
const (
	rootExitNoPassword = 91 // sudo needs a password and none was sent
	rootExitRejected   = 92 // the device did not accept the password (PAM's helper, or sudo)
	rootExitNoTicket   = 93 // sudo accepted the password but asks again for the next command
	rootExitNoSudo     = 94 // no sudo, or no way to hand it the password safely
	rootExitLocked     = 95 // the password is right (PAM's helper) and sudo still refused the login
)

// rootMark starts the stderr lines with which the wrapper talks to tnctl.
const rootMark = "@@tnctl-root:"

// Markers that are not a reason to give up.
const (
	rootMarkRun  = "run"  // the root shell: the script starts now
	rootMarkAsk  = "ask"  // the wrapper: what follows is `sudo -v` being given the password
	rootMarkAuth = "auth" // the wrapper: `sudo -v` accepted it; what it printed is of no interest
	rootMarkSudo = "sudo" // the wrapper: what follows is the sudo that runs the script
	// rootMarkFor is not the wrapper's but sudo's: the prompt `sudo -v` is given ("for %u %p").
	// sudo prints it when it asks, with the login and the user whose password it wants. The two
	// differ under targetpw, rootpw and runaspw, where the login's password is not the answer.
	rootMarkFor = "for"
)

type rootReason string

const (
	rootNoPassword rootReason = "no-password"
	rootRejected   rootReason = "rejected"
	rootNoTicket   rootReason = "no-ticket"
	rootNoSudo     rootReason = "no-sudo"
	rootNoPrintf   rootReason = "no-printf"
	rootLocked     rootReason = "locked"
	// rootRefused is sudo itself refusing to run the command; rootError.said is its reason.
	rootRefused rootReason = "refused"
)

var rootExitOf = map[rootReason]int{
	rootNoPassword: rootExitNoPassword,
	rootRejected:   rootExitRejected,
	rootNoTicket:   rootExitNoTicket,
	rootNoSudo:     rootExitNoSudo,
	rootNoPrintf:   rootExitNoSudo,
	rootLocked:     rootExitLocked,
}

// The two ways the wrapper is run; its third argument.
const (
	rootTry          = "try"      // `sudo -n` alone: no password is sent
	rootWithPassword = "password" // the first stdin line is the password
)

// Where the wrapper finds PAM's password helper and the local accounts. Variables for the unit
// tests only, which must not ask the helper of the machine they run on.
var (
	rootPAMHelpers = []string{"/usr/sbin/unix_chkpwd", "/sbin/unix_chkpwd"}
	rootPasswdFile = "/etc/passwd"
)

// rootWrapper is the script the login user's sh runs. $1 is the script to run as root, $2 the
// root shell's own script (rootStub), $3 the mode.
//
//   - With mode "try" it is `sudo -n` and nothing else. Nothing reads stdin before the script.
//   - With mode "password", read takes the first line of stdin and nothing more (sh reads a pipe
//     byte by byte). An empty line is never given to sudo: it counts as a failed attempt.
//   - Before sudo sees the password, PAM's own helper checks it (`unix_chkpwd <user> nullok`, the
//     password and a NUL on its stdin: what pam_unix runs for a process that is not root). It
//     answers 7 for a wrong password of a local account, and then sudo is never asked. This is
//     not a shortcut around sudo: a wrong password given to `sudo -S -v` is one line in sudo's
//     log but 2 failed logins for pam_faillock (sudo asks PAM again when its input has ended),
//     and 3 is where faillock's defaults lock the account. The helper counts nothing and writes
//     its own line to the auth log. It is trusted only for an account in /etc/passwd; without a
//     helper or a local account the decision is sudo's.
//   - The password then goes to `sudo -S -v` on a pipe of its own, one line and then EOF: no line
//     of the payload can be taken as a second guess. sudo reads it only if it needs a password.
//     printf must be the shell's builtin, or the password would be in a process's arguments.
//     What sudo says there is kept between the "ask" and "auth" markers: it is the reason when
//     sudo refuses (not in the sudoers, a locked login), and noise when it accepts.
//   - The prompt of that sudo is a marker line of its own with %u and %p in it (rootMarkFor):
//     sudo prints it only when it asks, and it says whose password it asks for. With targetpw or
//     rootpw that is root's, which tnctl does not have. By then the login's password has been
//     given to sudo once (the helper found it right for the login); that cannot be known before.
//   - If sudo refuses a password the helper found right, the login is locked or expired, not
//     wrong: an exit status of its own.
//   - Without a terminal sudo keeps the ticket for the parent process. Every sudo here is a child
//     of this one sh, so the ticket of -v is the one the last sudo finds. That is why the last
//     sudo is not exec'ed and why `exit` follows it (bash and zsh exec the last command of a -c
//     script, which makes the ticket unreachable: "sudo: a password is required"), and why
//     `sudo -v` is not inside a `$(...)`: that is another parent too.
//   - `sudo -n -v` then asks "is that ticket there for the next command?". It reads /dev/null and
//     never goes to PAM while the ticket is there. It is not `sudo -n true`: that also asks
//     whether `true` is allowed, which is not the question. Only when it fails is this a sudo
//     that keeps no ticket (timestamp_timeout=0).
//   - Every sudo runs under LC_ALL=C, so that what it says is what sudoSaid knows. The root shell
//     puts the login's own LC_ALL back before the script runs.
//   - No sudo but the last gets the wrapper's stdin: with `Defaults log_input` sudo copies its
//     stdin for the log and would take the payload that waits there.
//   - The "sudo" marker before the last sudo starts the lines that are read as sudo's answer to
//     the command: what ssh, its proxy or the login shell printed before it is not.
func rootWrapperFor(helpers []string, passwd string) string {
	mark := func(token string, code int) string {
		return fmt.Sprintf(`{ echo "%s%s" >&2; exit %d; }`, rootMark, token, code)
	}
	return `L=${LC_ALL-}
command -v sudo >/dev/null 2>&1 || ` + mark("no-sudo", rootExitNoSudo) + `
if [ "$3" = ` + rootWithPassword + ` ]; then
  IFS= read -r p || :
  [ -n "$p" ] || ` + mark("no-password", rootExitNoPassword) + `
  [ "$(command -v printf)" = printf ] || ` + mark("no-printf", rootExitNoSudo) + `
  u=$(id -un); l=; k=
  while IFS=: read -r n x; do [ "$n" != "$u" ] || l=1; done <` + passwd + `
  for c in ` + strings.Join(helpers, " ") + `; do
    [ -n "$l" ] && [ -x "$c" ] || continue
    # Only a helper with its setuid/setgid bit can read the shadow file; without it, PAM
    # before 1.4 answers 7 (wrong) for the right password too.
    [ -u "$c" ] || [ -g "$c" ] || continue
    printf "%s\0" "$p" | "$c" "$u" nullok >/dev/null 2>&1; k=$?; break
  done
  [ "$k" != 7 ] || ` + mark("rejected", rootExitRejected) + `
  echo "` + rootMark + rootMarkAsk + `" >&2
  printf "%s\n" "$p" | LC_ALL=C sudo -S -p "
` + rootMark + rootMarkFor + ` %u %p
" -v || {
    [ "$k" != 0 ] || ` + mark("locked", rootExitLocked) + `
    ` + mark("rejected", rootExitRejected) + `
  }
  unset p
  # A PAM prompt of its own ends without a line break: start the marker on a line of its own.
  echo >&2
  echo "` + rootMark + rootMarkAuth + `" >&2
  LC_ALL=C sudo -n -v </dev/null 2>/dev/null || ` + mark("no-ticket", rootExitNoTicket) + `
fi
echo "` + rootMark + rootMarkSudo + `" >&2
LC_ALL=C sudo -n sh -c "$2" tnctl-root "$1" "$L"
exit $?`
}

var rootWrapper = rootWrapperFor(rootPAMHelpers, rootPasswdFile)

// rootStub is what root's sh runs: $1 is the script, $2 the LC_ALL of the login. Its first line
// tells tnctl that sudo let the command through. The exec here is root's own shell becoming the
// script's shell; it is not the sudo of the wrapper, whose parent must stay.
const rootStub = `echo "` + rootMark + rootMarkRun + `" >&2; [ "${LC_ALL-}" != C ] || { LC_ALL=$2; [ -n "$2" ] || unset LC_ALL; }; exec sh -c "$1"`

// rootCommand is the remote command line that runs script as root. It holds no secret: the
// password travels on stdin only, and only in mode rootWithPassword.
func rootCommand(script, mode string) string {
	return "sh -c " + shq(rootWrapper) + " tnctl-root " + shq(script) + " " + shq(rootStub) + " " + mode
}

// rootFirstLine is the line that carries the password to the wrapper. A password with a line
// break or a NUL byte cannot travel as one line; it is not sent (unusable). ok is false when
// there is nothing to send.
func rootFirstLine(password string) (line string, unusable, ok bool) {
	if strings.ContainsAny(password, "\n\x00") {
		return "", true, false
	}
	if password == "" {
		return "", false, false
	}
	return password + "\n", false, true
}

// sudoWord is what sudo itself said about a command it did not run.
type sudoWord int

const (
	sudoSilent        sudoWord = iota // nothing of sudo's
	sudoOther                         // a line of sudo's that tnctl does not know
	sudoAsks                          // it needs a password
	sudoAccount                       // the login is locked or expired
	sudoNotAllowed                    // the sudoers do not let the login run this command
	sudoNotInSudoers                  // the login is not in the sudoers at all
	sudoNoTTY                         // requiretty
	sudoOtherPassword                 // it asks for another user's password (targetpw, rootpw, runaspw)
)

// sudoPrefixes start the lines sudo writes itself (sudo-rs 0.2.13 says "sudo: " too; "sudo-rs: "
// is for a build that names itself). What a PAM module says through sudo has no such prefix.
var sudoPrefixes = []string{"sudo: ", "sudo-rs: "}

// sudoSorry starts sudo's other lines ("Sorry, try again.", "Sorry, user tn may not run sudo").
const sudoSorry = "Sorry, "

// sudoAskLines are the lines with which sudo says that it needs a password, after its prefix:
// sudo 1.8 to 1.9, and sudo-rs. Only a line that is exactly one of them makes tnctl send the
// password, so the whole line must be sudo's sentence, not a text that has it somewhere.
var sudoAskLines = []string{"a password is required", "interactive authentication is required"}

// sudoSentences are sudo's other sentences, as sudo 1.8 to 1.9 and sudo-rs print them in English
// (the wrapper sets LC_ALL=C). Some have no prefix ("tn is not in the sudoers file."), and PAM's
// own words about an account come through sudo as they are, so a part of the line is enough
// here: sudoSaid is only given the lines that sudo wrote.
var sudoSentences = []struct {
	word  sudoWord
	frags []string
}{
	{sudoNoTTY, []string{"you must have a tty to run sudo"}},
	{sudoNotInSudoers, []string{"is not in the sudoers file", "may not run sudo on"}},
	{sudoNotAllowed, []string{"is not allowed to execute", "is not allowed to run sudo on", "I'm afraid I can't do that"}},
	{sudoAccount, []string{"account validation failure", "PAM account management error", "is expired", "has expired"}},
}

// maxSudoLine bounds the line of sudo's that is shown when tnctl does not know it.
const maxSudoLine = 200

// sudoSaid reads what sudo said about a command it did not run. said must be sudo's own lines
// and nothing else: the lines after the wrapper's "ask" and "sudo" markers (rootStderr.sudo).
// The rest of a failed run's stderr is ssh's, its proxy's or the login shell's, and a sentence
// there ("Your tnctl login has expired") says nothing about sudo. The last sentence it knows
// wins. line is the last line it does not know, for sudoOther: sudo warns first ("unable to
// resolve host") and gives its reason last. A line that starts the way sudo's do is taken before
// one that does not (what a PAM module printed through sudo).
func sudoSaid(said string) (word sudoWord, line string) {
	other := ""
	for _, l := range strings.Split(said, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		known, sudos := false, strings.HasPrefix(l, sudoSorry)
		for _, prefix := range sudoPrefixes {
			if rest, ok := strings.CutPrefix(l, prefix); ok {
				sudos = true
				for _, ask := range sudoAskLines {
					if rest == ask {
						word, known = sudoAsks, true
					}
				}
			}
		}
		for _, s := range sudoSentences {
			for _, frag := range s.frags {
				if strings.Contains(l, frag) {
					word, known = s.word, true
				}
			}
		}
		switch {
		case known:
		case sudos:
			line = l
		default:
			other = l
		}
	}
	if line == "" {
		line = other
	}
	if word == sudoSilent && line != "" {
		word = sudoOther
	}
	if len(line) > maxSudoLine {
		line = line[:maxSudoLine] + "..."
	}
	return word, line
}

// rootError says that the login user cannot become root on the device. It is final for the
// session: nothing is tried again (a rejected password must not be sent a second time; each
// attempt may count against the login on the device).
type rootError struct {
	reason rootReason
	device string
	// unusable: the platform has a password, but it cannot be sent as one line.
	unusable bool
	// bySudo, for rootRejected: sudo refused the password, and PAM's helper had not said whether
	// it is the login's (no helper, a login that is not local, a helper that cannot tell). sudo
	// refuses a locked login in the same words, so this is one of the two.
	bySudo bool
	// said is sudo's reason and detail its own line, for rootRefused. With sudoOtherPassword
	// detail is the user whose password sudo asks for.
	said   sudoWord
	detail string
}

func (e *rootError) Error() string {
	switch e.reason {
	case rootNoPassword:
		if e.unusable {
			return fmt.Sprintf("sudo on %s asks for a password, and the platform's password for the device login has a line break or a NUL byte in it, which cannot be sent to sudo", e.device)
		}
		return fmt.Sprintf("sudo on %s asks for a password, and the platform has none for the device login", e.device)
	case rootRejected:
		if e.bySudo {
			return fmt.Sprintf("sudo on %s did not accept the platform's password for the device login: the password on the device is another one, or the login is locked after failed attempts (PAM's helper on the device did not say which); it is not tried again", e.device)
		}
		return fmt.Sprintf("sudo on %s did not accept the platform's password for the device login (the password on the device is another one); it is not tried again", e.device)
	case rootLocked:
		return fmt.Sprintf("sudo on %s refused the device login although the platform's password for it is right: the login may be locked after failed attempts, or expired", e.device)
	case rootNoTicket:
		return fmt.Sprintf("sudo on %s accepted the password but asks for it again for the next command (timestamp_timeout=0 in its sudoers?), so tnctl cannot run commands as root there", e.device)
	case rootNoPrintf:
		return fmt.Sprintf("the sh on %s has no built-in printf, so the password cannot be handed to sudo without showing it in the process list", e.device)
	case rootRefused:
		switch e.said {
		case sudoNoTTY:
			return fmt.Sprintf("sudo on %s only works on a terminal (requiretty in its sudoers), and tnctl has none", e.device)
		case sudoNotInSudoers:
			return fmt.Sprintf("the device login user may not use sudo on %s (it is not in the sudoers there)", e.device)
		case sudoNotAllowed:
			return fmt.Sprintf("the sudoers on %s do not let the device login user run sh as root, which is how tnctl runs a command there", e.device)
		case sudoAsks:
			return fmt.Sprintf("sudo on %s accepted the device login and still asks for a password for the command: its sudoers do not let the device login user run sh as root", e.device)
		case sudoOtherPassword:
			return fmt.Sprintf("sudo on %s asks for the password of %s, not for the device login's (targetpw, rootpw or runaspw in its sudoers); the platform only has the login's, so tnctl cannot run commands as root there", e.device, e.detail)
		}
		return fmt.Sprintf("sudo on %s did not run the command: %s", e.device, e.detail)
	}
	return fmt.Sprintf("sudo is not installed on %s (or not on the login user's PATH)", e.device)
}

// isRootError reports whether err is the login user not getting root on the device.
func isRootError(err error) bool {
	var re *rootError
	return errors.As(err, &re)
}

// rootAttempt is one run of the wrapper.
type rootAttempt struct {
	code int
	// ran: the root shell said that the script started. Then code is the script's.
	ran bool
	// token is the wrapper's reason for giving up, pre the stderr from before the script started:
	// ssh's, its proxy's, the login shell's and sudo's, as it came. It is shown, never read.
	token string
	pre   string
	// sudo is the part of pre that sudo wrote (after the wrapper's "ask" or "sudo" marker): the
	// only text sudo's reason is read from.
	sudo string
	// gave: the wrapper gave the password to `sudo -v` (PAM's helper had not refused it).
	gave bool
	// other: sudo asked for the password of this user, who is not the login (its prompt said so).
	other string
}

// rootVerdict reads an attempt whose script did not start. It returns the error when root is not
// to be had, or asks when sudo only wants a password (mode rootTry). Neither means that the
// failure is not sudo's: the exit status and stderr are passed on as they are. That is always so
// when ssh itself failed (255: no connection, an expired tnctl login in the proxy, a connection
// that broke): whatever was printed until then, sudo has not answered, and nothing is final.
func rootVerdict(device string, a rootAttempt, mode string) (re *rootError, asks bool) {
	if a.ran || a.code == 0 || a.code == sshConnectFailed {
		return nil, false
	}
	said, line := sudoSaid(a.sudo)
	refused := func() *rootError {
		return &rootError{reason: rootRefused, device: device, said: said, detail: line}
	}
	reason := rootReason(a.token)
	if want, ok := rootExitOf[reason]; ok && want == a.code {
		if reason == rootRejected || reason == rootLocked {
			// `sudo -v` refused, and said why: not every refusal is the password's.
			if a.other != "" {
				said, line = sudoOtherPassword, a.other
				return refused(), false
			}
			switch said {
			case sudoNoTTY, sudoNotInSudoers, sudoNotAllowed:
				return refused(), false
			case sudoAccount:
				reason = rootLocked
			}
		}
		return &rootError{reason: reason, device: device, bySudo: reason == rootRejected && a.gave}, false
	}
	switch said {
	case sudoSilent:
		return nil, false
	case sudoAsks:
		if mode == rootTry {
			return nil, true
		}
		// `sudo -v` was content and the ticket is there, and sudo still asks for this command:
		// a login whose rules name single commands, none of them sh.
		return refused(), false
	case sudoAccount:
		return &rootError{reason: rootLocked, device: device}, false
	}
	return refused(), false
}

// rootStderr is the stderr of a wrapper run. Until the root shell's "run" line everything is
// held back (pre), and the marker lines are taken out; from that line on every byte is the
// script's and is passed on untouched, at once. Of what is held back, the lines after the
// wrapper's "ask" and "sudo" markers are also kept as sudo's own (sudo).
type rootStderr struct {
	w io.Writer
	// onRun is called once, when the script starts.
	onRun func()

	ran   bool
	token string
	pre   []byte
	line  []byte
	askAt int
	asked bool
	// sudo: see rootAttempt. inSudo: a sudo of the wrapper is running, its lines are sudo's.
	sudo   []byte
	inSudo bool
	gave   bool
	other  string
}

// maxRootPre bounds what is held back; more than this before the script starts is passed on.
const maxRootPre = 64 << 10

func (f *rootStderr) Write(p []byte) (int, error) {
	n := len(p)
	for !f.ran && len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			f.line = append(f.line, p...)
			p = nil
			break
		}
		f.line = append(f.line, p[:i]...)
		p = p[i+1:]
		line := string(f.line)
		f.line = f.line[:0]
		token, isMark := strings.CutPrefix(line, rootMark)
		switch {
		case !isMark:
			f.hold([]byte(line + "\n"))
		case token == rootMarkRun:
			f.ran = true
			if err := f.passPre(); err != nil {
				return 0, err
			}
			if f.onRun != nil {
				f.onRun()
			}
		case token == rootMarkAsk:
			f.askAt, f.asked, f.gave = len(f.pre), true, true
			f.sudo, f.inSudo, f.other = nil, true, ""
		case token == rootMarkAuth:
			if f.asked {
				f.pre, f.asked = f.pre[:f.askAt], false
			}
			f.sudo, f.inSudo, f.other = nil, false, ""
		case token == rootMarkSudo:
			f.sudo, f.inSudo = nil, true
		case strings.HasPrefix(token, rootMarkFor+" "):
			// sudo's prompt: "for <login> <the user whose password it asks for>". A sudo that
			// does not fill them in is not read.
			if who := strings.Fields(token); f.inSudo && len(who) == 3 && !strings.Contains(token, "%") && who[1] != who[2] {
				f.other = who[2]
			}
		default:
			f.token, f.inSudo = token, false
		}
	}
	if len(p) > 0 {
		if _, err := f.w.Write(p); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (f *rootStderr) hold(b []byte) {
	if len(f.pre)+len(b) > maxRootPre {
		_ = f.passPre()
	}
	f.pre = append(f.pre, b...)
	if f.inSudo {
		if len(f.sudo)+len(b) > maxRootPre {
			f.sudo = nil // sudo's reason is among its last lines
		}
		f.sudo = append(f.sudo, b...)
	}
}

// passPre writes what was held back to the script's stderr: it was not a refusal.
func (f *rootStderr) passPre() error {
	pre := f.pre
	f.pre, f.asked = nil, false
	if len(pre) == 0 {
		return nil
	}
	_, err := f.w.Write(pre)
	return err
}

// finish is called at the end of the stream; it returns the attempt as far as stderr tells it.
// Nothing was held back when the script started.
func (f *rootStderr) finish() rootAttempt {
	if f.ran {
		return rootAttempt{ran: true, token: f.token}
	}
	if len(f.line) > 0 {
		f.hold(f.line)
		f.line = nil
	}
	return rootAttempt{token: f.token, pre: string(f.pre), sudo: string(f.sudo), gave: f.gave, other: f.other}
}

// feedStdin gives cmd its stdin: first (the password line, or nothing) at once, and the payload
// only when gate closes, which is when the script has started. It returns the function to call
// once cmd has started. done ends the wait for an attempt whose script never started: stdin has
// then not been read at all and is whole for the next attempt. With a stdin of the caller the
// copy runs outside cmd: Wait must not wait for a terminal that nobody types on after the remote
// command has ended.
func feedStdin(cmd *exec.Cmd, first string, stdin io.Reader, gate, done <-chan struct{}) (func(), error) {
	if stdin == nil {
		if first != "" {
			cmd.Stdin = strings.NewReader(first)
		}
		return nil, nil
	}
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	return func() {
		go func() {
			defer func() { _ = pipe.Close() }()
			if _, err := io.WriteString(pipe, first); err != nil {
				return
			}
			select {
			case <-gate:
			case <-done:
				// The script may have started and ended in the same moment.
				select {
				case <-gate:
				default:
					return
				}
			}
			_, _ = io.Copy(pipe, stdin)
		}()
	}, nil
}

// rootFailed returns the root failure this session already ran into.
func (w *wgExec) rootFailed() error {
	w.rootMu.Lock()
	defer w.rootMu.Unlock()
	if w.rootErr != nil {
		return w.rootErr
	}
	return nil
}

// rootFail keeps the session's first root failure and returns it: every rootError goes through
// here, so that no later call asks sudo again.
func (w *wgExec) rootFail(re *rootError) error {
	w.rootMu.Lock()
	defer w.rootMu.Unlock()
	if w.rootErr == nil {
		w.rootErr = re
	}
	return w.rootErr
}

func (w *wgExec) sudoAsks() bool {
	w.rootMu.Lock()
	defer w.rootMu.Unlock()
	return w.rootAsks
}

// asRoot runs one root command through attempt: with `sudo -n` alone first, and with the
// password only when sudo asked for one, now or earlier in this session. attempt gets the mode,
// the first stdin line and the function to call when the script starts.
//
// Attempts go one at a time until their script has started: a password the device does not
// accept is then in one command, never in two that happen to run side by side.
func (w *wgExec) asRoot(attempt func(mode, first string, onRun func()) (rootAttempt, error)) (rootAttempt, error) {
	w.rootTurn.Lock()
	release := sync.OnceFunc(w.rootTurn.Unlock)
	defer release()
	if err := w.rootFailed(); err != nil {
		return rootAttempt{}, err
	}
	if !w.sudoAsks() {
		a, err := attempt(rootTry, "", release)
		if err != nil || a.ran {
			return a, err
		}
		re, asks := rootVerdict(w.sess.device, a, rootTry)
		if re != nil {
			return a, w.rootFail(re)
		}
		if !asks {
			return a, nil
		}
		w.rootMu.Lock()
		w.rootAsks = true
		w.rootMu.Unlock()
	}
	first, unusable, ok := rootFirstLine(w.sess.password)
	if !ok {
		// Nothing is sent: an empty line would be a failed attempt for sudo.
		return rootAttempt{code: rootExitNoPassword}, w.rootFail(&rootError{reason: rootNoPassword, device: w.sess.device, unusable: unusable})
	}
	a, err := attempt(rootWithPassword, first, release)
	if err != nil || a.ran {
		return a, err
	}
	if re, _ := rootVerdict(w.sess.device, a, rootWithPassword); re != nil {
		if code, ok := rootExitOf[re.reason]; ok {
			a.code = code
		}
		return a, w.rootFail(re)
	}
	return a, nil
}

// unreached is the error of a root command whose ssh ended with 255 before the script started:
// ssh's own failure (no connection, a ProxyCommand that gave up: "Your tnctl login has expired"),
// in the words ssh or its proxy said. The device was not reached, so this is not sudo's answer
// (rootVerdict) and not final: nothing is kept for the session, and the next call may connect.
func (w *wgExec) unreached(said string) error {
	return fmt.Errorf("cannot reach %s over WireGuard: %s", w.sess.device, firstNonEmptyLine(said))
}

// RunRoot runs a sh script as root and captures its output.
func (w *wgExec) RunRoot(ctx context.Context, script string) (execResult, error) {
	var res execResult
	a, err := w.asRoot(func(mode, first string, onRun func()) (rootAttempt, error) {
		cmd := w.command(ctx, rootCommand(script, mode), true)
		if first != "" {
			cmd.Stdin = strings.NewReader(first)
		}
		var out, errb bytes.Buffer
		filter := &rootStderr{w: &errb, onRun: onRun}
		cmd.Stdout, cmd.Stderr = &out, filter
		err := cmd.Run()
		a := filter.finish()
		res = execResult{Stdout: out.String(), Stderr: errb.String() + a.pre}
		var ee *exec.ExitError
		switch {
		case err == nil:
			return a, nil
		case ctx.Err() != nil:
			return a, ctx.Err()
		case errors.As(err, &ee):
			a.code = ee.ExitCode()
			if a.code == sshConnectFailed && !a.ran {
				return a, w.unreached(a.pre)
			}
			return a, nil
		}
		return a, err
	})
	if isRootError(err) {
		// What sudo said is in the error; it is not the script's output.
		res = execResult{}
	}
	res.Code = a.code
	return res, err
}

// StreamRoot runs a sh script as root with the given streams; stdin (may be nil) is what the
// script reads.
func (w *wgExec) StreamRoot(ctx context.Context, script string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	a, err := w.asRoot(func(mode, first string, onRun func()) (rootAttempt, error) {
		cmd := w.command(ctx, rootCommand(script, mode), false)
		gate, done := make(chan struct{}), make(chan struct{})
		filter := &rootStderr{w: stderr, onRun: func() {
			close(gate)
			onRun()
		}}
		cmd.Stdout, cmd.Stderr = stdout, filter
		started, err := feedStdin(cmd, first, stdin, gate, done)
		if err != nil {
			return rootAttempt{}, err
		}
		err = runForwarding(cmd, started)
		close(done)
		a := filter.finish()
		var ee *exec.ExitError
		switch {
		case err == nil:
			return a, nil
		case errors.As(err, &ee):
			a.code = ee.ExitCode()
			if a.code == sshConnectFailed && !a.ran {
				// The first line ssh said is in the error; when it said more, all of it is shown.
				if strings.Contains(strings.TrimSpace(a.pre), "\n") {
					_, _ = io.WriteString(stderr, a.pre)
				}
				return a, w.unreached(a.pre)
			}
			return a, nil
		}
		return a, err
	})
	if err == nil && !a.ran && a.pre != "" {
		// Not sudo's refusal and not ssh's failure (a broken shell): it is shown as it came.
		_, _ = io.WriteString(stderr, a.pre)
	}
	return a.code, err
}

// rootProbe runs a probe script as root. The probe scripts end in `true` and print a section
// marker before each answer, so any other exit status, or no marker at all, means that the
// script did not do its work: that is an error, never an empty answer (which would read as "the
// object is not on the robot"). Whether sudo refused is not read from the output here: RunRoot
// knows it (a *rootError, which is final), and a script that ran owns every line it printed.
func rootProbe(ctx context.Context, ex deviceExec, script string) (execResult, error) {
	res, err := ex.RunRoot(ctx, script)
	if err != nil {
		return res, err
	}
	if res.Code != 0 {
		line := firstNonEmptyLine(res.Stderr)
		if line == "" {
			line = "no error message"
		}
		return res, fmt.Errorf("the check on the robot ended with exit status %d: %s", res.Code, line)
	}
	if strings.Contains(script, sectionMark) && !strings.Contains(res.Stdout, sectionMark) {
		return res, errors.New("the check on the robot printed nothing")
	}
	return res, nil
}

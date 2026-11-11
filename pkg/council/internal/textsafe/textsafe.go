// Package textsafe holds what the council must never let travel: the bot's own key, which the
// platform redacts per event, and the redaction the council applies before a text leaves it.
package textsafe

import "regexp"

// BotKeyPattern is a TeknoirBot key: tbk1.<id>.<id>.<43 characters>.
var BotKeyPattern = regexp.MustCompile(`tbk1\.[a-z0-9-]{1,63}\.[a-z0-9-]{1,63}\.[A-Za-z0-9_-]{43}`)

// KeyPrefixPattern is a text that runs to its end with a bot key's start: tbk1. followed by the
// ids written only in part, or not at all. What follows it may still become a key.
var KeyPrefixPattern = regexp.MustCompile(`^tbk1\.(?:[a-z0-9-]{0,63}|[a-z0-9-]{1,63}\.(?:[a-z0-9-]{0,63}|[a-z0-9-]{1,63}\.[A-Za-z0-9_-]{0,42}))$`)

// keyTailPattern is a key the text ends inside: a stream that ended before the key did.
var keyTailPattern = regexp.MustCompile(`tbk1\.[a-z0-9-]{1,63}\.[a-z0-9-]{1,63}\.[A-Za-z0-9_-]{1,42}$`)

// Redacted is what a secret becomes.
const Redacted = "[redacted]"

// RedactBotKeys replaces every bot key in s with [redacted].
func RedactBotKeys(s string) string {
	return BotKeyPattern.ReplaceAllString(s, Redacted)
}

// RedactKeyTail replaces a key cut short at the end of s with [redacted]: a stream that ends inside
// a key would otherwise leave a piece no redaction can see.
func RedactKeyTail(s string) string {
	return keyTailPattern.ReplaceAllString(s, Redacted)
}

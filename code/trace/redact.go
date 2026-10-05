package trace

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/tsengmingchun/quill/backend"
)

const redacted = "[REDACTED]"

// secretTokens matches well-known credential formats wherever they show up
// in a trace (tool output, file contents, model replies). A token must
// start a word; inside encoded JSON a word can also follow an escaped
// newline or tab, where \b would not match.
var secretTokens = regexp.MustCompile(`(^|[^A-Za-z0-9_]|\\[nrt])(?:` +
	`sk-[A-Za-z0-9_-]{20,}` + // OpenAI, OpenRouter, Anthropic
	`|ctx7sk-[A-Za-z0-9-]{20,}` + // Context7
	`|gh[pousr]_[A-Za-z0-9]{30,}` + // GitHub
	`|github_pat_[A-Za-z0-9_]{30,}` + // GitHub fine-grained
	`|AKIA[0-9A-Z]{16}` + // AWS access key id
	`|xox[abposr]-[A-Za-z0-9-]{10,}` + // Slack
	`|AIza[0-9A-Za-z_-]{35}` + // Google
	`)`)

// urlPassword matches the password of scheme://user:password@host.
var urlPassword = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^:/\s"\\@]*:)[^@\s"\\/]+@`)

// redactor removes secrets from encoded trace lines before they are stored.
type redactor struct {
	// values are the JSON-encoded forms of secret environment values, as
	// they appear inside encoded strings.
	values [][]byte
}

// newRedactor redacts the values of credential-named variables in environ
// plus well-known token formats.
func newRedactor(environ []string) redactor {
	var r redactor
	for _, v := range backend.SecretEnvValues(environ) {
		enc, err := json.Marshal(v)
		if err != nil {
			continue
		}
		r.values = append(r.values, enc[1:len(enc)-1])
	}
	return r
}

// line returns line with secrets replaced; the JSON stays valid because
// only string contents are replaced, by a string without escapes.
func (r redactor) line(line []byte) []byte {
	for _, v := range r.values {
		line = bytes.ReplaceAll(line, v, []byte(redacted))
	}
	line = secretTokens.ReplaceAll(line, []byte("${1}"+redacted))
	return urlPassword.ReplaceAll(line, []byte("${1}"+redacted+"@"))
}

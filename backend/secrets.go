package backend

import (
	"regexp"
	"strings"
)

// secretEnvName matches environment variable names that usually hold
// credentials: OPENROUTER_API_KEY, GITHUB_TOKEN, AWS_SECRET_ACCESS_KEY,
// POSTGRES_CONN_STRING, ...
var secretEnvName = regexp.MustCompile(`(?i)(?:^|_)(?:API_?KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIALS?|PRIVATE_KEY|ACCESS_KEY|CONN_STRING)(?:_|$)`)

// minSecretLen keeps short values (flags, "true") out of value redaction.
const minSecretLen = 8

// SecretEnvName reports whether an environment variable name looks like it
// holds a credential.
func SecretEnvName(name string) bool { return secretEnvName.MatchString(name) }

// SecretEnvValues returns the values of credential-named variables in
// environ ("KEY=value" entries) that are long enough to redact.
func SecretEnvValues(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if ok && len(value) >= minSecretLen && SecretEnvName(name) {
			out = append(out, value)
		}
	}
	return out
}

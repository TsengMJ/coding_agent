package agent

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

// dangerousShellPatterns are substrings that can smuggle extra commands past
// the allow list (Python `DANGEROUS_SHELL_PATTERNS`).
var dangerousShellPatterns = []string{
	"$(",  // command substitution
	"`",   // backtick substitution
	"$'",  // ANSI-C quoting
	"\n",  // newline
	"\r",  // carriage return
	"\t",  // tab
	"<(",  // process substitution (input)
	">(",  // process substitution (output)
	"<<<", // here-string
	"<<",  // here-doc
	">>",  // append redirect
	">",   // output redirect
	"<",   // input redirect
	"${",  // brace expansion
}

var (
	bareVariable     = regexp.MustCompile(`\$[A-Za-z_]`)
	commandSeparator = regexp.MustCompile(`&&|\|\||[|;]`)
)

// containsDangerousPatterns reports whether command uses substitution,
// redirection, variable expansion, control characters, or a background `&`
// (Python `contains_dangerous_patterns`).
func containsDangerousPatterns(command string) bool {
	for _, p := range dangerousShellPatterns {
		if strings.Contains(command, p) {
			return true
		}
	}
	return bareVariable.MatchString(command) || hasStandaloneAmpersand(command)
}

// hasStandaloneAmpersand finds an `&` that is not part of `&&`.
func hasStandaloneAmpersand(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '&' {
			continue
		}
		prev := i > 0 && s[i-1] == '&'
		next := i+1 < len(s) && s[i+1] == '&'
		if !prev && !next {
			return true
		}
	}
	return false
}

// IsShellCommandAllowed reports whether every command in a pipeline or list
// starts with an allow-listed executable (Python `is_shell_command_allowed`).
// allowAll approves any non-empty command without further checks.
func IsShellCommandAllowed(command string, allow []string, allowAll bool) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}
	if allowAll {
		return true
	}
	if len(allow) == 0 || containsDangerousPatterns(command) {
		return false
	}
	found := false
	for _, segment := range commandSeparator.Split(command, -1) {
		if strings.TrimSpace(segment) == "" {
			continue
		}
		words, err := splitShellWords(segment)
		if err != nil {
			return false
		}
		if len(words) == 0 {
			continue
		}
		if !slices.Contains(allow, words[0]) {
			return false
		}
		found = true
	}
	return found
}

var errUnbalancedQuote = errors.New("unbalanced quote")

// splitShellWords splits s like POSIX shlex.split: whitespace separates
// words, single quotes are literal, and a backslash escapes the next character
// outside quotes.
func splitShellWords(s string) ([]string, error) {
	var (
		words   []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			// Inside double quotes a backslash only escapes `"` and `\`.
			if quote == '"' && r != '"' && r != '\\' {
				cur.WriteRune('\\')
			}
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, errUnbalancedQuote
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

package commands

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrNotCommand is returned by Dispatch for input that is not a slash command.
var ErrNotCommand = errors.New("not a slash command")

// UnknownCommandError reports a slash command with no registered handler.
type UnknownCommandError struct {
	Name string
	// Suggestion is the closest registered name, or "".
	Suggestion string
}

func (e *UnknownCommandError) Error() string {
	msg := fmt.Sprintf("unknown command %s", e.Name)
	if e.Suggestion != "" {
		msg += fmt.Sprintf(" — did you mean %s?", e.Suggestion)
	}
	return msg + " (try /help)"
}

// Registry maps command names and aliases to handlers.
type Registry struct {
	cmds     []Command
	handlers map[string]Handler
	byName   map[string]Command
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Handler{}, byName: map[string]Command{}}
}

// Register binds a handler to a command and its aliases. Names must start
// with "/" and may be registered only once.
func (r *Registry) Register(cmd Command, h Handler) error {
	if h == nil {
		return fmt.Errorf("commands: nil handler for %s", cmd.Name)
	}
	names := append([]string{cmd.Name}, cmd.Aliases...)
	for _, n := range names {
		if !strings.HasPrefix(n, "/") || strings.ContainsAny(n, " \t") {
			return fmt.Errorf("commands: invalid command name %q", n)
		}
		if _, dup := r.handlers[n]; dup {
			return fmt.Errorf("commands: %s is already registered", n)
		}
	}
	for _, n := range names {
		r.handlers[n] = h
		r.byName[n] = cmd
	}
	r.cmds = append(r.cmds, cmd)
	return nil
}

// Lookup resolves a name or alias to its command and handler.
func (r *Registry) Lookup(name string) (Command, Handler, bool) {
	h, ok := r.handlers[name]
	return r.byName[name], h, ok
}

// Parse splits "/name args" into the command name and trimmed arguments.
func Parse(input string) (name, args string, ok bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return "", "", false
	}
	name, args, _ = strings.Cut(input, " ")
	return name, strings.TrimSpace(args), true
}

// Dispatch parses "/name args" and runs the matching handler. Input that
// does not start with "/" returns ErrNotCommand; an unregistered name
// returns *UnknownCommandError.
func (r *Registry) Dispatch(ctx context.Context, input string) error {
	name, args, ok := Parse(input)
	if !ok {
		return ErrNotCommand
	}
	_, h, found := r.Lookup(name)
	if !found {
		return &UnknownCommandError{Name: name, Suggestion: r.Suggest(name)}
	}
	return h(ctx, args)
}

// Commands returns the registered, non-hidden commands sorted by name.
func (r *Registry) Commands() []Command {
	return Visible(r.cmds)
}

// Visible returns the non-hidden commands in cmds sorted by name.
func Visible(cmds []Command) []Command {
	var out []Command
	for _, c := range cmds {
		if !c.Hidden {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Command) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Help renders the registered, non-hidden commands one per line.
func (r *Registry) Help() string {
	return Help(r.Commands())
}

// Help renders cmds as an aligned "commands:" listing.
func Help(cmds []Command) string {
	width := 0
	usage := func(c Command) string {
		if c.Args != "" {
			return c.Name + " " + c.Args
		}
		return c.Name
	}
	for _, c := range cmds {
		width = max(width, len(usage(c)))
	}
	var b strings.Builder
	b.WriteString("commands:")
	for _, c := range cmds {
		line := fmt.Sprintf("\n  %-*s  %s", width, usage(c), c.Description)
		if len(c.Aliases) > 0 {
			line += " (" + strings.Join(c.Aliases, ", ") + ")"
		}
		b.WriteString(strings.TrimRight(line, " "))
	}
	return b.String()
}

// Suggest returns the registered non-hidden name closest to name: a unique
// prefix match, otherwise the nearest within two edits. "" when none is close.
func (r *Registry) Suggest(name string) string {
	var names []string
	for n, c := range r.byName {
		if !c.Hidden {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	var prefixed []string
	for _, n := range names {
		if strings.HasPrefix(n, name) {
			prefixed = append(prefixed, n)
		}
	}
	if len(prefixed) == 1 {
		return prefixed[0]
	}
	best, bestDist := "", 3
	for _, n := range names {
		if d := editDistance(name, n); d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

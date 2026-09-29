// Package skills discovers user, project and built-in skills for the CLI and
// turns them into slash commands (Python `deepagents_code/skills/*`).
package skills

import (
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	core "github.com/tsengmingchun/deepagents_golang/middleware/skills"
)

// Location is where a skill was found.
type Location string

const (
	LocationBuiltIn Location = "built-in"
	LocationUser    Location = "user"
	LocationProject Location = "project"
	LocationPlugin  Location = "plugin"
)

// Skill is a discovered skill with provenance.
type Skill struct {
	core.Metadata
	Location Location
	// Trusted is false for project skills the user has not approved.
	Trusted bool
}

// Discover lists skills from the given directories; later locations override earlier ones.
func Discover(dirs map[Location]string) ([]Skill, error) {
	return nil, notimpl.Err("code/skills.Discover")
}

// InvocationPrompt builds the prompt sent when a skill is run as /skill-name.
func InvocationPrompt(s Skill, args string) (string, error) {
	return "", notimpl.Err("code/skills.InvocationPrompt")
}

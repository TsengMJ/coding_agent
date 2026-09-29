package runtime

import "fmt"

// Interrupt pauses a run and surfaces Value to the caller (LangGraph `interrupt()`).
type Interrupt struct {
	ID    string
	Value any
}

// InterruptError is returned from a run that paused on one or more interrupts.
// Resume the thread by invoking again with a Command carrying Resume values.
type InterruptError struct {
	Interrupts []Interrupt
}

func (e *InterruptError) Error() string {
	return fmt.Sprintf("run interrupted: %d pending interrupt(s)", len(e.Interrupts))
}

// Command is returned by tools/nodes to update state and control flow, or passed
// by callers to resume an interrupted run (LangGraph `Command`).
type Command struct {
	Update *Update
	// Resume maps interrupt IDs to resume values.
	Resume map[string]any
	// Goto optionally names the next node ("model", "tools", "end").
	Goto string
}

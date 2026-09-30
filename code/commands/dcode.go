package commands

// DCode lists the commands dcode implements. The TUI and line REPL register
// handlers for all of them; the GUI reads this list (GET /commands) for
// autocomplete and help.
var DCode = []Command{
	{Name: "/new", Aliases: []string{"/clear"}, Description: "start a new thread"},
	{Name: "/thread", Description: "show the current thread id"},
	{Name: "/threads", Description: "list threads in this directory"},
	{Name: "/resume", Args: "<id>", Description: "switch to another thread"},
	{Name: "/compact", Aliases: []string{"/offload"}, Description: "summarize older messages to free context"},
	{Name: "/model", Args: "[provider:model] [--default]", Description: "show or switch the model; --default also saves it to config.toml"},
	{Name: "/yolo", Description: "toggle auto-approval of tool calls"},
	{Name: "/help", Description: "show this help"},
	{Name: "/quit", Aliases: []string{"/exit", "/q"}, Description: "exit"},
}

// Find returns the command in cmds named name (not an alias).
func Find(cmds []Command, name string) (Command, bool) {
	for _, c := range cmds {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

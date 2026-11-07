package harnessadapter

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

// commandSet collects what a harness reports about its commands. Harness
// output is hostile: entries with unusable names are dropped, text is folded
// to one bounded line, later duplicates lose, and the list stops at the
// protocol limit.
type commandSet struct {
	commands []adapterproto.Command
	names    map[string]bool
}

func newCommandSet() *commandSet {
	return &commandSet{names: map[string]bool{}}
}

func (c *commandSet) add(
	name, description, hint string, kind adapterproto.CommandKind, aliases ...string,
) {
	if len(c.commands) >= adapterproto.MaxCommands ||
		!adapterproto.ValidCommandName(name) || c.names[name] {
		return
	}
	c.names[name] = true
	command := adapterproto.Command{
		Name:         name,
		Description:  commandLine(description, adapterproto.MaxCommandDescriptionBytes),
		ArgumentHint: commandLine(hint, adapterproto.MaxCommandHintBytes),
		Kind:         kind,
	}
	for _, alias := range aliases {
		if len(command.Aliases) >= adapterproto.MaxCommandAliases ||
			!adapterproto.ValidCommandName(alias) || c.names[alias] {
			continue
		}
		c.names[alias] = true
		command.Aliases = append(command.Aliases, alias)
	}
	c.commands = append(c.commands, command)
}

func (c *commandSet) has(name string) bool {
	return c.names[name]
}

func (c *commandSet) list() []adapterproto.Command {
	if len(c.commands) == 0 {
		return nil
	}
	return append([]adapterproto.Command(nil), c.commands...)
}

// commandLine folds whitespace and drops control characters, then truncates
// on a rune boundary.
func commandLine(value string, limit int) string {
	var result strings.Builder
	space := false
	for _, r := range value {
		switch {
		case unicode.IsSpace(r):
			space = result.Len() > 0
			continue
		case unicode.IsControl(r) || r == utf8.RuneError:
			continue
		}
		if space {
			result.WriteByte(' ')
			space = false
		}
		result.WriteRune(r)
	}
	text := result.String()
	if len(text) <= limit {
		return text
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// slashCommand splits "/name arguments" typed by the user. It reports false
// for text that does not start with a usable command name.
func slashCommand(content string) (string, string, bool) {
	rest, found := strings.CutPrefix(content, "/")
	if !found {
		return "", "", false
	}
	name, arguments, _ := strings.Cut(rest, " ")
	if index := strings.IndexAny(name, "\t\r\n"); index >= 0 {
		name, arguments = rest[:index], rest[index+1:]
	}
	if !adapterproto.ValidCommandName(name) {
		return "", "", false
	}
	return name, strings.TrimSpace(arguments), true
}

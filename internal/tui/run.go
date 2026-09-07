package tui

import (
	"context"
	"errors"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/OrlojHQ/meridian/internal/localrepo"
)

// Run launches the interactive dashboard. It emits no terminal control
// sequences unless both input and output are native terminals.
func Run(
	ctx context.Context,
	api API,
	server, tokenFile string,
	input *os.File,
	output io.Writer,
	preferredHarness string,
) error {
	if input == nil {
		input = os.Stdin
	}
	if output == nil {
		output = os.Stdout
	}
	outputFile, ok := output.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(outputFile.Fd())) {
		return errors.New("meridian tui requires terminal stdin and stdout; use CLI commands with --json for noninteractive access")
	}
	current, _ := localrepo.Discover(ctx, "")
	model := NewModel(Options{
		Context: ctx, API: api, Server: server, TokenFile: tokenFile,
		SuggestedProjectName: current.Name, SuggestedRepositoryURL: current.OriginURL,
		PreferredHarness: preferredHarness,
	})
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(input), tea.WithOutput(output))
	_, err := program.Run()
	return err
}

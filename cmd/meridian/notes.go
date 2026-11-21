package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

// Project notes and working notes (ADR 0060).

const maxNoteFileBytes = 32 << 10

func newNotesCommand(config *cliConfig) *cobra.Command {
	root := &cobra.Command{
		Use:   "notes",
		Short: "Read and write Project notes and review agents' proposals",
		Long: "Project notes are Markdown files that reach every Thread of a Project as files.\n" +
			"Only you write them; agents propose changes, which you review one at a time.",
	}
	list := &cobra.Command{
		Use: "list PROJECT_ID", Short: "List a Project's notes", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ListProjectNotes(command.Context(), client.ListProjectNotesParams{ProjectId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.NoteList)
			if !ok {
				return responseError(result)
			}
			return writeNoteList(config, page)
		},
	}
	var revision int64
	show := &cobra.Command{
		Use: "show PROJECT_ID NAME", Short: "Print a note, or one revision with --revision", Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			params := client.GetProjectNoteParams{ProjectId: args[0], NoteName: noteName(args[1])}
			if revision > 0 {
				params.Revision = client.NewOptInt64(revision)
			}
			result, err := api.GetProjectNote(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			note, ok := result.(*client.NoteContent)
			if !ok {
				return responseError(result)
			}
			return writeNoteContent(config, note)
		},
	}
	show.Flags().Int64Var(&revision, "revision", 0, "a retained revision to print")
	edit := newNoteEditCommand(config, false)
	history := &cobra.Command{
		Use: "history PROJECT_ID NAME", Short: "List a note's revisions with provenance", Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.ListProjectNoteRevisions(command.Context(),
				client.ListProjectNoteRevisionsParams{ProjectId: args[0], NoteName: noteName(args[1])})
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.NoteHistory)
			if !ok {
				return responseError(result)
			}
			return writeNoteHistory(config, page)
		},
	}
	restore := &cobra.Command{
		Use: "restore PROJECT_ID NAME REVISION", Short: "Make a retained revision the note's text again",
		Args: cobra.ExactArgs(3),
		RunE: func(command *cobra.Command, args []string) error {
			var from int64
			if _, err := fmt.Sscan(args[2], &from); err != nil || from < 1 {
				return errors.New("REVISION must be a revision number")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			current, err := currentRevision(command, api, args[0], "", noteName(args[1]))
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.RestoreProjectNote(command.Context(),
				&client.RestoreNoteRequest{Revision: from, ExpectedRevision: current},
				client.RestoreProjectNoteParams{ProjectId: args[0], NoteName: noteName(args[1]), IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			summary, ok := result.(*client.NoteSummary)
			if !ok {
				return responseError(result)
			}
			return writeNoteSummary(config, summary)
		},
	}
	var withHistory bool
	remove := &cobra.Command{
		Use: "delete PROJECT_ID NAME", Short: "Delete a note; its history stays unless --with-history",
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			current, err := currentRevision(command, api, args[0], "", noteName(args[1]))
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.DeleteProjectNote(command.Context(),
				&client.DeleteNoteRequest{ExpectedRevision: current, WithHistory: client.NewOptBool(withHistory)},
				client.DeleteProjectNoteParams{ProjectId: args[0], NoteName: noteName(args[1]), IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			summary, ok := result.(*client.NoteSummary)
			if !ok {
				return responseError(result)
			}
			return writeNoteSummary(config, summary)
		},
	}
	remove.Flags().BoolVar(&withHistory, "with-history", false, "also drop every revision's content and pending proposals")
	var all bool
	proposals := &cobra.Command{
		Use: "proposals [PROJECT_ID]", Short: "List note proposals waiting for review", Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			var result any
			if len(args) == 0 {
				result, err = api.ListPendingNoteProposals(command.Context())
			} else {
				result, err = api.ListProjectNoteProposals(command.Context(), client.ListProjectNoteProposalsParams{
					ProjectId: args[0], Pending: client.NewOptBool(!all),
				})
			}
			if err != nil {
				return apiCallError(err)
			}
			page, ok := result.(*client.NoteProposalList)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, page)
			}
			for _, item := range page.Items {
				if err := writeProposalLine(config.stdout, item); err != nil {
					return err
				}
			}
			return nil
		},
	}
	proposals.Flags().BoolVar(&all, "all", false, "include proposals that are no longer pending")
	review := &cobra.Command{
		Use: "proposal PROPOSAL_ID", Short: "Show one proposal and the note's current text",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.GetNoteProposal(command.Context(), client.GetNoteProposalParams{ProposalId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			item, ok := result.(*client.NoteProposalReview)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, item)
			}
			return writeProposalReview(config.stdout, item)
		},
	}
	var digest, editedFile string
	approve := &cobra.Command{
		Use:   "approve PROPOSAL_ID --digest DIGEST",
		Short: "Approve one proposal you reviewed",
		Long: "Approve one proposal. --digest is the digest `meridian notes proposal` printed, so the\n" +
			"approval binds exactly the text you read. --file approves your edited text instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.GetNoteProposal(command.Context(), client.GetNoteProposalParams{ProposalId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			item, ok := result.(*client.NoteProposalReview)
			if !ok {
				return responseError(result)
			}
			request := &client.ApproveNoteProposalRequest{
				Digest: digest, ExpectedRevision: item.Proposal.CurrentRevision,
			}
			if editedFile != "" {
				content, err := readNoteFile(config.stdin, editedFile)
				if err != nil {
					return err
				}
				request.Content = client.NewOptString(content)
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			approved, err := api.ApproveNoteProposal(command.Context(), request,
				client.ApproveNoteProposalParams{ProposalId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			summary, ok := approved.(*client.NoteProposalSummary)
			if !ok {
				return responseError(approved)
			}
			if config.json {
				return writeJSON(config.stdout, summary)
			}
			return writeProposalLine(config.stdout, *summary)
		},
	}
	approve.Flags().StringVar(&digest, "digest", "", "the reviewed proposal's digest")
	approve.Flags().StringVar(&editedFile, "file", "", "approve this edited text instead (- for stdin)")
	_ = approve.MarkFlagRequired("digest")
	reject := &cobra.Command{
		Use: "reject PROPOSAL_ID", Short: "Reject one proposal", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.RejectNoteProposal(command.Context(),
				client.RejectNoteProposalParams{ProposalId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			summary, ok := result.(*client.NoteProposalSummary)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, summary)
			}
			return writeProposalLine(config.stdout, *summary)
		},
	}
	delivery := &cobra.Command{
		Use: "delivery PROJECT_ID all_threads|leads_only|off", Short: "Choose which Threads receive Project notes",
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			listed, err := api.ListProjectNotes(command.Context(), client.ListProjectNotesParams{ProjectId: args[0]})
			if err != nil {
				return apiCallError(err)
			}
			page, ok := listed.(*client.NoteList)
			if !ok {
				return responseError(listed)
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.SetProjectNotesDelivery(command.Context(),
				&client.NotesDeliveryRequest{NotesDelivery: client.NotesDelivery(args[1]), ExpectedVersion: page.SettingsVersion},
				client.SetProjectNotesDeliveryParams{ProjectId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			setting, ok := result.(*client.NotesDeliverySetting)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, setting)
			}
			_, err = fmt.Fprintf(config.stdout, "notes-delivery=%s\n", setting.NotesDelivery)
			return err
		},
	}
	var confirm bool
	shred := &cobra.Command{
		Use: "shred PROJECT_ID --yes", Short: "Crypto-shred every Project note and proposal", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if !confirm {
				return errors.New("shredding cannot be undone; pass --yes")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.ShredProjectNotes(command.Context(), client.ShredProjectNotesParams{ProjectId: args[0], IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			if _, ok := result.(*client.ShredProjectNotesNoContent); !ok {
				return responseError(result)
			}
			_, err = fmt.Fprintln(config.stdout, "shredded")
			return err
		},
	}
	shred.Flags().BoolVar(&confirm, "yes", false, "confirm the shred")
	root.AddCommand(list, show, edit, history, restore, remove, proposals, review, approve, reject, delivery, shred)
	return root
}

// newGrantNotesCommand lists or prints a lead tree's working notes.
func newGrantNotesCommand(config *cliConfig) *cobra.Command {
	var revision int64
	notes := &cobra.Command{
		Use: "notes GRANT_ID [NAME]", Short: "List a lead tree's working notes, or print one",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				result, err := api.ListWorkingNotes(command.Context(), client.ListWorkingNotesParams{GrantId: args[0]})
				if err != nil {
					return apiCallError(err)
				}
				page, ok := result.(*client.NoteList)
				if !ok {
					return responseError(result)
				}
				return writeNoteList(config, page)
			}
			params := client.GetWorkingNoteParams{GrantId: args[0], NoteName: noteName(args[1])}
			if revision > 0 {
				params.Revision = client.NewOptInt64(revision)
			}
			result, err := api.GetWorkingNote(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			note, ok := result.(*client.NoteContent)
			if !ok {
				return responseError(result)
			}
			return writeNoteContent(config, note)
		},
	}
	notes.Flags().Int64Var(&revision, "revision", 0, "a retained revision to print")
	return notes
}

func newNoteEditCommand(config *cliConfig, _ bool) *cobra.Command {
	var file string
	var expected int64
	command := &cobra.Command{
		Use:   "edit PROJECT_ID NAME [--file PATH|-]",
		Short: "Write a note from a file, stdin, or $EDITOR",
		Long: "Replace a Project note. Without --file, the note opens in $EDITOR. The write is\n" +
			"refused if someone changed the note since you opened it.",
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			name := noteName(args[1])
			current, text, err := currentNote(command, api, args[0], name)
			if err != nil {
				return err
			}
			if command.Flags().Changed("expected-revision") {
				current = expected
			}
			var content string
			if file != "" {
				content, err = readNoteFile(config.stdin, file)
			} else {
				content, err = editInEditor(text)
			}
			if err != nil {
				return err
			}
			key, err := idempotencyKey("")
			if err != nil {
				return err
			}
			result, err := api.PutProjectNote(command.Context(),
				&client.WriteNoteRequest{Content: content, ExpectedRevision: current},
				client.PutProjectNoteParams{ProjectId: args[0], NoteName: name, IdempotencyKey: key})
			if err != nil {
				return apiCallError(err)
			}
			summary, ok := result.(*client.NoteSummary)
			if !ok {
				return responseError(result)
			}
			return writeNoteSummary(config, summary)
		},
	}
	command.Flags().StringVar(&file, "file", "", "read the note from this file, or - for stdin")
	command.Flags().Int64Var(&expected, "expected-revision", 0, "the revision you read (default: the current one)")
	return command
}

func noteName(value string) string { return strings.TrimSuffix(value, ".md") }

// currentNote returns a Project note's head revision and text, or 0 and
// nothing when the note does not exist or was deleted.
func currentNote(command *cobra.Command, api *client.Client, project, name string) (int64, string, error) {
	result, err := api.GetProjectNote(command.Context(), client.GetProjectNoteParams{ProjectId: project, NoteName: name})
	if err != nil {
		return 0, "", apiCallError(err)
	}
	if note, ok := result.(*client.NoteContent); ok {
		return note.Note.Revision, note.Content, nil
	}
	if envelope, ok := result.(*client.ErrorEnvelope); ok && envelope.Error.Code == "not_found" {
		return currentHead(command, api, project, name)
	}
	return 0, "", responseError(result)
}

// currentHead finds a deleted note's head in the list, so a write against
// it names the right revision.
func currentHead(command *cobra.Command, api *client.Client, project, name string) (int64, string, error) {
	result, err := api.ListProjectNotes(command.Context(), client.ListProjectNotesParams{ProjectId: project})
	if err != nil {
		return 0, "", apiCallError(err)
	}
	page, ok := result.(*client.NoteList)
	if !ok {
		return 0, "", responseError(result)
	}
	for _, item := range page.Items {
		if item.Name == name {
			return item.Revision, "", nil
		}
	}
	return 0, "", nil
}

func currentRevision(command *cobra.Command, api *client.Client, project, _ string, name string) (int64, error) {
	revision, _, err := currentHead(command, api, project, name)
	if err == nil && revision == 0 {
		return 0, fmt.Errorf("no note named %s", name)
	}
	return revision, err
}

func readNoteFile(stdin io.Reader, path string) (string, error) {
	var reader io.Reader = stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxNoteFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxNoteFileBytes {
		return "", fmt.Errorf("a note is at most %d bytes", maxNoteFileBytes)
	}
	return string(data), nil
}

// editInEditor opens text in $EDITOR in a private temporary file.
func editInEditor(text string) (string, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return "", errors.New("set $EDITOR or pass --file")
	}
	dir, err := os.MkdirTemp("", "meridian-note-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	fields := strings.Fields(editor)
	run := exec.Command(fields[0], append(fields[1:], path)...)
	run.Stdin, run.Stdout, run.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := run.Run(); err != nil {
		return "", fmt.Errorf("editor: %w", err)
	}
	return readNoteFile(nil, path)
}

// escapeHidden makes control, bidirectional and zero-width characters
// visible, so agent text cannot hide in a terminal.
func escapeHidden(text string) string {
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0),
			r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c,
			r >= 0x200b && r <= 0x200d, r == 0x2060, r == 0xfeff, unicode.Is(unicode.Cf, r):
			fmt.Fprintf(&b, "\\u{%04X}", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func writeNoteList(config *cliConfig, page *client.NoteList) error {
	if config.json {
		return writeJSON(config.stdout, page)
	}
	for _, item := range page.Items {
		if err := writeNoteLine(config.stdout, item); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(config.stdout, "total=%d/%d bytes notes=%d/%d delivery=%s pending-proposals=%d\n",
		page.TotalBytes, page.MaxBytes, page.LiveCount, page.MaxNotes, page.NotesDelivery, page.PendingProposals)
	return err
}

func writeNoteLine(writer io.Writer, item client.NoteSummary) error {
	state := "live"
	if !item.Live {
		state = "deleted"
	}
	label := ""
	if item.AgentProposed {
		label = " agent-proposed"
	}
	by := string(item.Writer)
	if name, ok := item.LinkName.Get(); ok && name != "" {
		by = name
	}
	_, err := fmt.Fprintf(writer, "%s.md\trev=%d\t%s\tsize=%d\tby=%s%s\t%s\n",
		item.Name, item.Revision, state, item.Size, by, label, item.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	return err
}

func writeNoteSummary(config *cliConfig, item *client.NoteSummary) error {
	if config.json {
		return writeJSON(config.stdout, item)
	}
	return writeNoteLine(config.stdout, *item)
}

func writeNoteContent(config *cliConfig, note *client.NoteContent) error {
	if config.json {
		return writeJSON(config.stdout, note)
	}
	if note.CredentialShaped {
		fmt.Fprintln(os.Stderr, "warning: this note contains credential-shaped content")
	}
	_, err := io.WriteString(config.stdout, escapeHidden(note.Content))
	return err
}

func writeNoteHistory(config *cliConfig, page *client.NoteHistory) error {
	if config.json {
		return writeJSON(config.stdout, page)
	}
	for _, item := range page.Items {
		retained := "retained"
		if !item.Retained {
			retained = "content-dropped"
		}
		source := ""
		if value, ok := item.Source.Get(); ok {
			if name, ok := value.LinkName.Get(); ok && name != "" {
				source = " link=" + name
			}
			if thread, ok := value.ThreadId.Get(); ok && thread != "" {
				source += " thread=" + thread
			}
		}
		if _, err := fmt.Fprintf(config.stdout, "rev=%d\t%s\t%s\t%s\tsize=%d\t%s%s\t%s\n", item.Revision, item.Name,
			item.Kind, item.Writer, item.Size, retained, source, item.CreatedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
			return err
		}
	}
	return nil
}

func writeProposalLine(writer io.Writer, item client.NoteProposalSummary) error {
	action := "write"
	if item.Delete {
		action = "delete"
	}
	thread := ""
	if source, ok := item.Source.Get(); ok {
		if value, ok := source.ThreadId.Get(); ok {
			thread = " thread=" + value
		}
	}
	_, err := fmt.Fprintf(writer, "%s\tproject=%s\t%s.md\t%s\t%s\tbase=%d current=%d%s\n",
		item.ID, item.ProjectId, item.Name, action, item.State, item.BaseRevision, item.CurrentRevision, thread)
	return err
}

func writeProposalReview(writer io.Writer, item *client.NoteProposalReview) error {
	if err := writeProposalLine(writer, item.Proposal); err != nil {
		return err
	}
	fmt.Fprintf(writer, "digest=%s\n", item.Proposal.Digest)
	if item.CredentialShaped {
		fmt.Fprintln(writer, "warning: the proposal contains credential-shaped content")
	}
	fmt.Fprintln(writer, "--- current (operator-reviewed)")
	fmt.Fprint(writer, escapeHidden(item.CurrentContent))
	fmt.Fprintln(writer, "\n+++ proposed (untrusted agent text)")
	_, err := fmt.Fprintln(writer, escapeHidden(item.Content))
	return err
}

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/OrlojHQ/meridian/internal/buildinfo"
	"github.com/OrlojHQ/meridian/internal/tui"
	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

type cliConfig struct {
	server string
	json   bool
	stdout io.Writer
	stdin  io.Reader
}

func newRootCommand() *cobra.Command {
	info := buildinfo.Current()
	config := &cliConfig{stdout: os.Stdout, stdin: os.Stdin}
	command := &cobra.Command{
		Use:           "meridian",
		Short:         "Manage Meridian development environments",
		Long:          "Manage projects and Capsules through the Meridian API.",
		Version:       info.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.SetVersionTemplate("meridian {{.Version}}\n")
	command.PersistentFlags().StringVar(&config.server, "server", "http://127.0.0.1:8080", "Meridian API URL")
	command.PersistentFlags().BoolVar(&config.json, "json", false, "write stable API JSON")
	command.AddCommand(
		newProjectCommand(config), newCapsuleCommand(config), newRunCommand(config),
		newThreadCommand(config),
		newMomentCommand(config), newTimelineCommand(config), newShardCommand(config),
		newRewindCommand(config), newSealCommand(config), newTUICommand(config),
		newAdminCommand(config),
	)
	return command
}

func newTUICommand(config *cliConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive Capsule dashboard",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if config.json {
				return errors.New("--json is not supported by the interactive dashboard; use scriptable resource commands")
			}
			input, ok := config.stdin.(*os.File)
			if !ok {
				return errors.New("meridian tui requires terminal stdin and stdout")
			}
			api, err := tui.NewAPI(config.server)
			if err != nil {
				return err
			}
			return tui.Run(command.Context(), api, config.server, input, config.stdout)
		},
	}
}

func newProjectCommand(config *cliConfig) *cobra.Command {
	project := &cobra.Command{Use: "project", Short: "Manage projects"}

	var createKey, repositoryURL, imageReference string
	var setup []string
	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			key, err := idempotencyKey(createKey)
			if err != nil {
				return err
			}
			input := &client.CreateProjectRequest{Name: args[0], Setup: setup}
			if repositoryURL != "" {
				input.RepositoryUrl = client.NewOptString(repositoryURL)
			}
			if imageReference != "" {
				input.ImageReference = client.NewOptString(imageReference)
			}
			result, err := api.CreateProject(
				command.Context(),
				input,
				client.CreateProjectParams{IdempotencyKey: key},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ProjectHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeProject(success.Response)
		},
	}
	create.Flags().StringVar(&createKey, "idempotency-key", "", "mutation replay key (generated if omitted)")
	create.Flags().StringVar(&repositoryURL, "repository-url", "", "public URL or absolute local fixture repository path")
	create.Flags().StringArrayVar(&setup, "setup-arg", nil, "setup argv element; repeat in executable-first order")
	create.Flags().StringVar(&imageReference, "image", "", "project Capsule image reference")

	var listCursor string
	var listLimit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			params := client.ListProjectsParams{Limit: client.NewOptInt(listLimit)}
			if listCursor != "" {
				params.Cursor = client.NewOptString(listCursor)
			}
			result, err := api.ListProjects(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ProjectPage)
			if !ok {
				return responseError(result)
			}
			return config.writeProjectPage(*success)
		},
	}
	list.Flags().StringVar(&listCursor, "cursor", "", "pagination cursor")
	list.Flags().IntVar(&listLimit, "limit", 50, "page size")

	get := &cobra.Command{
		Use:   "get PROJECT_ID",
		Short: "Get a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			result, err := api.GetProject(
				command.Context(), client.GetProjectParams{ProjectId: args[0]},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.ProjectHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeProject(success.Response)
		},
	}
	project.AddCommand(create, list, get)
	return project
}

func newCapsuleCommand(config *cliConfig) *cobra.Command {
	capsule := &cobra.Command{Use: "capsule", Short: "Manage Capsules"}

	var createKey string
	create := &cobra.Command{
		Use:   "create PROJECT_ID NAME",
		Short: "Request Capsule creation",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			key, err := idempotencyKey(createKey)
			if err != nil {
				return err
			}
			result, err := api.CreateCapsule(
				command.Context(),
				&client.CreateCapsuleRequest{Name: args[1]},
				client.CreateCapsuleParams{ProjectId: args[0], IdempotencyKey: key},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CapsuleHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeCapsule(success.Response)
		},
	}
	create.Flags().StringVar(&createKey, "idempotency-key", "", "mutation replay key (generated if omitted)")

	var listCursor string
	var listLimit int
	list := &cobra.Command{
		Use:   "list PROJECT_ID",
		Short: "List a project's Capsules",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			params := client.ListCapsulesParams{
				ProjectId: args[0],
				Limit:     client.NewOptInt(listLimit),
			}
			if listCursor != "" {
				params.Cursor = client.NewOptString(listCursor)
			}
			result, err := api.ListCapsules(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CapsulePage)
			if !ok {
				return responseError(result)
			}
			return config.writeCapsulePage(*success)
		},
	}
	list.Flags().StringVar(&listCursor, "cursor", "", "pagination cursor")
	list.Flags().IntVar(&listLimit, "limit", 50, "page size")

	get := &cobra.Command{
		Use:   "get CAPSULE_ID",
		Short: "Get a Capsule",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			result, err := api.GetCapsule(
				command.Context(), client.GetCapsuleParams{CapsuleId: args[0]},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CapsuleHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeCapsule(success.Response)
		},
	}

	capsule.AddCommand(create, list, get)
	capsule.AddCommand(newLifecycleCommand(config, "pause"), newLifecycleCommand(config, "resume"))
	capsule.AddCommand(newLifecycleCommand(config, "delete"))
	capsule.AddCommand(newCapsuleGitCommand(config, false), newCapsuleGitCommand(config, true))
	return capsule
}

func newLifecycleCommand(config *cliConfig, operation string) *cobra.Command {
	var expected int64
	var keyFlag string
	command := &cobra.Command{
		Use:   operation + " CAPSULE_ID",
		Short: "Request Capsule " + operation,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expected <= 0 {
				return errors.New("--expected-version must be greater than zero")
			}
			api, err := newAPI(config.server)
			if err != nil {
				return err
			}
			key, err := idempotencyKey(keyFlag)
			if err != nil {
				return err
			}
			request := &client.LifecycleMutationRequest{ExpectedResourceVersion: expected}
			var result any
			switch operation {
			case "pause":
				result, err = api.PauseCapsule(
					command.Context(), request,
					client.PauseCapsuleParams{CapsuleId: args[0], IdempotencyKey: key},
				)
			case "resume":
				result, err = api.ResumeCapsule(
					command.Context(), request,
					client.ResumeCapsuleParams{CapsuleId: args[0], IdempotencyKey: key},
				)
			case "delete":
				result, err = api.DeleteCapsule(
					command.Context(), request,
					client.DeleteCapsuleParams{CapsuleId: args[0], IdempotencyKey: key},
				)
			}
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.CapsuleAcceptedHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeCapsule(success.Response)
		},
	}
	command.Flags().Int64Var(&expected, "expected-version", 0, "required current resource version")
	command.Flags().StringVar(&keyFlag, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func newAPI(server string) (*client.Client, error) {
	return client.NewClient(server)
}

func idempotencyKey(value string) (string, error) {
	if value != "" {
		return value, nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

func apiCallError(err error) error {
	var response *client.DefaultErrorStatusCode
	if errors.As(err, &response) {
		return fmt.Errorf("%s: %s", response.Response.Error.Code, response.Response.Error.Message)
	}
	return fmt.Errorf("API request failed: %w", err)
}

func responseError(response any) error {
	encoded, err := json.Marshal(response)
	if err == nil {
		var envelope client.ErrorEnvelope
		if json.Unmarshal(encoded, &envelope) == nil && envelope.Error.Code != "" {
			return fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
		}
	}
	return fmt.Errorf("API returned an unexpected response")
}

func (c *cliConfig) writeProject(project client.Project) error {
	if c.json {
		return writeJSON(c.stdout, project)
	}
	_, err := fmt.Fprintf(c.stdout, "%s\t%s\tversion=%d\n", project.ID, project.Name, project.ResourceVersion)
	return err
}

func (c *cliConfig) writeCapsule(capsule client.Capsule) error {
	if c.json {
		return writeJSON(c.stdout, capsuleOutput(capsule))
	}
	_, err := fmt.Fprintf(
		c.stdout, "%s\t%s\tstate=%s\tdesired=%s\tversion=%d\n",
		capsule.ID, capsule.Name, capsule.State, capsule.DesiredState, capsule.ResourceVersion,
	)
	return err
}

func (c *cliConfig) writeProjectPage(page client.ProjectPage) error {
	if c.json {
		output := projectPageOutput{Items: page.Items}
		output.NextCursor, _ = page.NextCursor.Get()
		return writeJSON(c.stdout, output)
	}
	for _, project := range page.Items {
		if err := c.writeProject(project); err != nil {
			return err
		}
	}
	if cursor, ok := page.NextCursor.Get(); ok {
		_, err := fmt.Fprintf(c.stdout, "next-cursor=%s\n", cursor)
		return err
	}
	return nil
}

func (c *cliConfig) writeCapsulePage(page client.CapsulePage) error {
	if c.json {
		output := capsulePageOutput{Items: make([]capsuleJSON, len(page.Items))}
		for i := range page.Items {
			output.Items[i] = capsuleOutput(page.Items[i])
		}
		output.NextCursor, _ = page.NextCursor.Get()
		return writeJSON(c.stdout, output)
	}
	for _, capsule := range page.Items {
		if err := c.writeCapsule(capsule); err != nil {
			return err
		}
	}
	if cursor, ok := page.NextCursor.Get(); ok {
		_, err := fmt.Fprintf(c.stdout, "next-cursor=%s\n", cursor)
		return err
	}
	return nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

type projectPageOutput struct {
	Items      []client.Project `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type capsulePageOutput struct {
	Items      []capsuleJSON `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type capsuleJSON struct {
	ID              string               `json:"id"`
	ProjectID       string               `json:"projectId"`
	Name            string               `json:"name"`
	State           client.CapsuleState  `json:"state"`
	DesiredState    client.CapsuleIntent `json:"desiredState"`
	Failure         string               `json:"failure,omitempty"`
	CreatedAt       time.Time            `json:"createdAt"`
	UpdatedAt       time.Time            `json:"updatedAt"`
	ResourceVersion int64                `json:"resourceVersion"`
}

func capsuleOutput(capsule client.Capsule) capsuleJSON {
	failure, _ := capsule.Failure.Get()
	return capsuleJSON{
		ID:              capsule.ID,
		ProjectID:       capsule.ProjectId,
		Name:            capsule.Name,
		State:           capsule.State,
		DesiredState:    capsule.DesiredState,
		Failure:         failure,
		CreatedAt:       capsule.CreatedAt,
		UpdatedAt:       capsule.UpdatedAt,
		ResourceVersion: capsule.ResourceVersion,
	}
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

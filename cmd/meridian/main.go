package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/buildinfo"
	"github.com/OrlojHQ/meridian/internal/tui"
	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

type cliConfig struct {
	server    string
	tokenFile string
	json      bool
	stdout    io.Writer
	stdin     io.Reader
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
	command.PersistentFlags().StringVar(
		&config.tokenFile, "token-file", defaultAPITokenFile(),
		"installation API token file (default MERIDIAN_TOKEN_FILE or local daemon data-dir)",
	)
	command.PersistentFlags().BoolVar(&config.json, "json", false, "write stable API JSON")
	command.AddCommand(
		newProjectCommand(config), newCapsuleCommand(config), newRunCommand(config),
		newSecretCommand(config),
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
			security, err := newSecuritySource(config.tokenFile)
			if err != nil {
				return err
			}
			api, err := tui.NewAPI(config.server, security)
			if err != nil {
				return err
			}
			return tui.Run(
				command.Context(), api, config.server, config.tokenFile, input, config.stdout,
			)
		},
	}
}

func newProjectCommand(config *cliConfig) *cobra.Command {
	project := &cobra.Command{Use: "project", Short: "Manage projects"}

	var createKey, repositoryURL, imageReference, gitSecretName string
	var gitPushSecretName, githubAPISecretName, commitAuthorName string
	var commitAuthorEmail, defaultBaseBranch string
	var setup, harnessSecretNames []string
	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			key, err := idempotencyKey(createKey)
			if err != nil {
				return err
			}
			input := &client.CreateProjectRequest{
				Name: args[0], Setup: setup, HarnessSecretNames: harnessSecretNames,
			}
			if repositoryURL != "" {
				input.RepositoryUrl = client.NewOptString(repositoryURL)
			}
			if imageReference != "" {
				input.ImageReference = client.NewOptString(imageReference)
			}
			if gitSecretName != "" {
				input.GitSecretName = client.NewOptString(gitSecretName)
			}
			if gitPushSecretName != "" {
				input.GitPushSecretName = client.NewOptString(gitPushSecretName)
			}
			if githubAPISecretName != "" {
				input.GithubAPISecretName = client.NewOptString(githubAPISecretName)
			}
			if commitAuthorName != "" {
				input.CommitAuthorName = client.NewOptString(commitAuthorName)
			}
			if commitAuthorEmail != "" {
				input.CommitAuthorEmail = client.NewOptString(commitAuthorEmail)
			}
			if defaultBaseBranch != "" {
				input.DefaultBaseBranch = client.NewOptString(defaultBaseBranch)
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
	create.Flags().StringVar(&gitSecretName, "git-secret", "", "authorized git_https secret name")
	create.Flags().StringVar(&gitPushSecretName, "git-push-secret", "", "authorized git_push secret name")
	create.Flags().StringVar(&githubAPISecretName, "github-api-secret", "", "authorized github_api secret name")
	create.Flags().StringVar(&commitAuthorName, "commit-author-name", "", "Delivery commit author name")
	create.Flags().StringVar(&commitAuthorEmail, "commit-author-email", "", "Delivery commit author email")
	create.Flags().StringVar(&defaultBaseBranch, "default-base-branch", "", "pull request base branch")
	create.Flags().StringArrayVar(
		&harnessSecretNames, "harness-secret", nil,
		"authorized harness_env secret name; repeat for each name",
	)

	var listCursor string
	var listLimit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := newAPI(config)
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
			api, err := newAPI(config)
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

func newSecretCommand(config *cliConfig) *cobra.Command {
	secret := &cobra.Command{Use: "secret", Short: "Manage encrypted named secrets"}
	var purpose, username, putKey string
	var expected int64
	var stdin bool
	put := &cobra.Command{
		Use:   "put NAME",
		Short: "Read a secret value from stdin and encrypt it",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			switch purpose {
			case "git_https", "git_push", "github_api", "harness_env":
			default:
				return errors.New("--purpose must be git_https, git_push, github_api, or harness_env")
			}
			if !stdin {
				return errors.New("--stdin is required; plaintext flags and arguments are not accepted")
			}
			value, err := io.ReadAll(io.LimitReader(config.stdin, (256<<10)+1))
			if err != nil {
				return fmt.Errorf("read secret from stdin: %w", err)
			}
			if len(value) > 256<<10 {
				return errors.New("secret exceeds 256 KiB")
			}
			key, err := idempotencyKey(putKey)
			if err != nil {
				return err
			}
			input := &client.PutSecretRequest{
				Purpose: client.PutSecretRequestPurpose(purpose),
			}
			if expected > 0 {
				input.ExpectedResourceVersion = client.NewOptInt64(expected)
			}
			if purpose == "git_https" || purpose == "git_push" {
				input.Password = client.NewOptString(string(value))
				if username != "" {
					input.Username = client.NewOptString(username)
				}
			} else {
				if username != "" {
					return errors.New("--username is only valid for Git secrets")
				}
				input.Value = client.NewOptString(string(value))
			}
			for index := range value {
				value[index] = 0
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.PutSecret(command.Context(), input, client.PutSecretParams{
				SecretName: args[0], IdempotencyKey: key,
			})
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.SecretHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeSecret(success.Response)
		},
	}
	put.Flags().StringVar(&purpose, "purpose", "", "secret purpose")
	put.Flags().BoolVar(&stdin, "stdin", false, "read the secret value from stdin")
	put.Flags().StringVar(&username, "username", "", "Git username (encrypted)")
	put.Flags().Int64Var(&expected, "expected-version", 0, "current version when replacing")
	put.Flags().StringVar(&putKey, "idempotency-key", "", "mutation replay key (generated if omitted)")

	var cursor string
	var limit int
	list := &cobra.Command{
		Use: "list", Short: "List secret metadata", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			params := client.ListSecretsParams{Limit: client.NewOptInt(limit)}
			if cursor != "" {
				params.Cursor = client.NewOptString(cursor)
			}
			result, err := api.ListSecrets(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.SecretPage)
			if !ok {
				return responseError(result)
			}
			if config.json {
				return writeJSON(config.stdout, success)
			}
			for _, item := range success.Items {
				if err := config.writeSecret(item); err != nil {
					return err
				}
			}
			return nil
		},
	}
	list.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	list.Flags().IntVar(&limit, "limit", 50, "page size")

	var deleteVersion int64
	var deleteKey string
	var confirm bool
	deleteCommand := &cobra.Command{
		Use: "delete NAME", Short: "Irreversibly delete a secret envelope",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if !confirm {
				return errors.New("--confirm-crypto-shred is required; deletion is irreversible")
			}
			if deleteVersion <= 0 {
				return errors.New("--expected-version is required")
			}
			key, err := idempotencyKey(deleteKey)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			result, err := api.DeleteSecret(
				command.Context(),
				&client.DeleteSecretRequest{ExpectedResourceVersion: deleteVersion},
				client.DeleteSecretParams{SecretName: args[0], IdempotencyKey: key},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := result.(*client.Secret)
			if !ok {
				return responseError(result)
			}
			return config.writeSecret(*success)
		},
	}
	deleteCommand.Flags().Int64Var(&deleteVersion, "expected-version", 0, "current secret version")
	deleteCommand.Flags().StringVar(&deleteKey, "idempotency-key", "", "mutation replay key (generated if omitted)")
	deleteCommand.Flags().BoolVar(&confirm, "confirm-crypto-shred", false, "confirm irreversible envelope deletion")
	secret.AddCommand(put, list, deleteCommand)
	return secret
}

func (c *cliConfig) writeSecret(secret client.Secret) error {
	if c.json {
		return writeJSON(c.stdout, secret)
	}
	_, err := fmt.Fprintf(c.stdout, "%s\t%s\tpurpose=%s\tversion=%d\n",
		secret.ID, secret.Name, secret.Purpose, secret.ResourceVersion)
	return err
}

func newCapsuleCommand(config *cliConfig) *cobra.Command {
	capsule := &cobra.Command{Use: "capsule", Short: "Manage Capsules"}

	var createKey string
	create := &cobra.Command{
		Use:   "create PROJECT_ID NAME",
		Short: "Request Capsule creation",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
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
			api, err := newAPI(config)
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
			api, err := newAPI(config)
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
	capsule.AddCommand(
		newCapsuleFilesCommand(config), newCapsuleSyncCommand(config), newCapsuleShipCommand(config),
	)
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
			api, err := newAPI(config)
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

type bearerSecuritySource struct {
	token string
}

func (s bearerSecuritySource) BearerAuth(
	context.Context,
	client.OperationName,
) (client.BearerAuth, error) {
	return client.BearerAuth{Token: s.token}, nil
}

func newAPI(config *cliConfig) (*client.Client, error) {
	security, err := newSecuritySource(config.tokenFile)
	if err != nil {
		return nil, err
	}
	return client.NewClient(config.server, security)
}

func newSecuritySource(path string) (client.SecuritySource, error) {
	token, err := apiauth.ReadTokenFile(path)
	if err != nil {
		return nil, fmt.Errorf("read API token file %q: %w", path, err)
	}
	return bearerSecuritySource{token: token}, nil
}

func defaultAPITokenFile() string {
	if path := os.Getenv("MERIDIAN_TOKEN_FILE"); path != "" {
		return path
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		directory = filepath.Join(".", "data")
	} else {
		directory = filepath.Join(directory, "meridian")
	}
	return apiauth.DefaultTokenPath(directory)
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

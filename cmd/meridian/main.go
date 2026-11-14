package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/buildinfo"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/localrepo"
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
		newSecretCommand(config), newHarnessCommand(config),
		newThreadCommand(config),
		newMomentCommand(config), newTimelineCommand(config), newShardCommand(config),
		newRewindCommand(config), newSealCommand(config), newTUICommand(config),
		newAdminCommand(config), newGrantCommand(config), newApprovalsCommand(config), newBoardCommand(config),
		newAuditCommand(config), newMemberCommand(config), newLoginCommand(config),
	)
	return command
}

func newTUICommand(config *cliConfig) *cobra.Command {
	var harness string
	command := &cobra.Command{
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
				strings.TrimSpace(harness),
			)
		},
	}
	command.Flags().StringVar(
		&harness, "harness", "",
		"select this installation pack on New Project; opens that form when no Projects exist",
	)
	return command
}

func projectCreateDefaults(
	ctx context.Context,
	args []string,
	repositoryURL string,
	repositoryFlagSet bool,
) (string, string, error) {
	var discovered localrepo.Repository
	var err error
	if repositoryFlagSet {
		repositoryURL, err = localrepo.Normalize(repositoryURL)
		if err != nil {
			return "", "", err
		}
	} else {
		discovered, err = localrepo.Discover(ctx, "")
		if err == nil {
			repositoryURL = discovered.OriginURL
		}
	}

	name := ""
	if len(args) > 0 {
		name = args[0]
	} else if discovered.Name != "" {
		name = discovered.Name
	} else {
		name = localrepo.Name(repositoryURL)
	}
	if name == "" {
		return "", "", errors.New("project NAME is required outside a Git worktree with an origin")
	}
	return name, repositoryURL, nil
}

func newProjectCommand(config *cliConfig) *cobra.Command {
	project := &cobra.Command{Use: "project", Short: "Manage projects"}

	var createKey, repositoryURL, imageReference, gitSecretName string
	var gitPushSecretName, githubAPISecretName, commitAuthorName string
	var commitAuthorEmail, defaultBaseBranch string
	var setup, harnessSecretNames, harnessImages []string
	create := &cobra.Command{
		Use:   "create [NAME]",
		Short: "Create a project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			name, resolvedRepositoryURL, err := projectCreateDefaults(
				command.Context(), args, repositoryURL,
				command.Flags().Changed("repository-url"),
			)
			if err != nil {
				return err
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			key, err := idempotencyKey(createKey)
			if err != nil {
				return err
			}
			input := &client.CreateProjectRequest{
				Name: name, Setup: setup, HarnessSecretNames: harnessSecretNames,
			}
			if resolvedRepositoryURL != "" {
				input.RepositoryUrl = client.NewOptString(resolvedRepositoryURL)
			}
			if imageReference != "" {
				input.ImageReference = client.NewOptString(imageReference)
			}
			if len(harnessImages) > 0 {
				input.HarnessImages = make([]client.HarnessImage, 0, len(harnessImages))
				for _, spec := range harnessImages {
					item, err := domain.ParseHarnessImageSpec(spec)
					if err != nil {
						return err
					}
					input.HarnessImages = append(input.HarnessImages, client.HarnessImage{
						Name: item.Name, ImageReference: item.ImageReference,
					})
				}
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
	create.Flags().StringVar(
		&repositoryURL, "repository-url", "",
		"repository URL or GitHub owner/repo (default current Git origin; pass empty to disable)",
	)
	create.Flags().StringArrayVar(&setup, "setup-arg", nil, "setup argv element; repeat in executable-first order")
	create.Flags().StringVar(&imageReference, "image", "", "default Capsule image reference")
	create.Flags().StringArrayVar(
		&harnessImages, "harness-image", nil,
		"allowlisted harness image as name=image; repeat for each harness",
	)
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
	apply := &cobra.Command{
		Use:   "apply-harness PROJECT_ID NAME|NAME=IMAGE",
		Short: "Allowlist a harness pack or custom harness image on a Project",
		Long: "Allowlist a harness on a Project, replacing any entry with the same name.\n" +
			"NAME applies the installation catalog's pack. NAME=IMAGE allowlists a custom image\n" +
			"under the profile name that Capsules and Threads will select, such as mock-structured.",
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			got, err := api.GetProject(
				command.Context(), client.GetProjectParams{ProjectId: args[0]},
			)
			if err != nil {
				return apiCallError(err)
			}
			project, ok := got.(*client.ProjectHeaders)
			if !ok {
				return responseError(got)
			}
			var pack client.HarnessImage
			if strings.Contains(args[1], "=") {
				item, err := domain.ParseHarnessImageSpec(args[1])
				if err != nil {
					return err
				}
				pack = client.HarnessImage{Name: item.Name, ImageReference: item.ImageReference}
			} else {
				capabilities, err := api.GetCapabilities(command.Context())
				if err != nil {
					return apiCallError(err)
				}
				catalog, ok := capabilities.(*client.Capabilities)
				if !ok {
					return responseError(capabilities)
				}
				for _, item := range catalog.HarnessImages {
					if item.Name == args[1] {
						pack = item
						break
					}
				}
				if pack.Name == "" {
					return fmt.Errorf("installation has no harness pack %q; use NAME=IMAGE for a custom image", args[1])
				}
			}
			// Interaction modes are catalog metadata a Project never stores.
			pack.InteractionModes = nil
			images := append([]client.HarnessImage(nil), project.Response.HarnessImages...)
			replaced := false
			for index, item := range images {
				if item.Name == pack.Name {
					images[index] = pack
					replaced = true
					break
				}
			}
			if !replaced {
				images = append(images, pack)
			}
			patched, err := api.PatchProject(
				command.Context(),
				&client.PatchProjectRequest{
					ExpectedResourceVersion: project.Response.ResourceVersion,
					HarnessImages:           images,
				},
				client.PatchProjectParams{ProjectId: args[0]},
			)
			if err != nil {
				return apiCallError(err)
			}
			success, ok := patched.(*client.ProjectHeaders)
			if !ok {
				return responseError(patched)
			}
			return config.writeProject(success.Response)
		},
	}

	var deleteYes bool
	var deleteKey string
	deleteProject := &cobra.Command{
		Use:   "delete PROJECT",
		Short: "Delete a project and everything it owns",
		Long: "Delete a project by id or name. Capsules are removed through the Capsule delete path, " +
			"including owned containers and volumes. Threads, transcripts, artifacts, the project environment, " +
			"and project-scoped secrets are deleted. A named secret is destroyed when no other live project still uses it. " +
			"The command prints what will be removed and requires --yes or the project name typed at the prompt.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			project, err := resolveProject(command.Context(), api, args[0])
			if err != nil {
				return err
			}
			capsules, threads, err := countProjectDependents(command.Context(), api, project.ID)
			if err != nil {
				return err
			}
			secrets := projectSecretCount(project)
			if !config.json {
				if _, err := fmt.Fprintf(
					config.stdout,
					"Delete project %s (%s)?\nThis permanently removes:\n  %d Capsules\n  %d threads\n  %d secrets\nTranscripts and artifacts are deleted with the project.\n",
					project.Name, project.ID, capsules, threads, secrets,
				); err != nil {
					return err
				}
			}
			if !deleteYes {
				if !config.json {
					if _, err := fmt.Fprint(config.stdout, "Type the project name to confirm: "); err != nil {
						return err
					}
				}
				reader := bufio.NewReader(config.stdin)
				line, err := reader.ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				if strings.TrimRight(line, "\r\n") != project.Name {
					return errors.New("confirmation did not match the project name; nothing was deleted")
				}
			}
			key, err := idempotencyKey(deleteKey)
			if err != nil {
				return err
			}
			result, err := api.DeleteProject(
				command.Context(),
				&client.LifecycleMutationRequest{ExpectedResourceVersion: project.ResourceVersion},
				client.DeleteProjectParams{ProjectId: project.ID, IdempotencyKey: key},
			)
			if err != nil {
				return apiCallError(err)
			}
			deleted, ok := result.(*client.ProjectHeaders)
			if !ok {
				return responseError(result)
			}
			return config.writeProject(deleted.Response)
		},
	}
	deleteProject.Flags().BoolVar(&deleteYes, "yes", false, "confirm deletion of the project and everything it owns")
	deleteProject.Flags().StringVar(&deleteKey, "idempotency-key", "", "mutation replay key (generated if omitted)")

	project.AddCommand(create, list, get, apply, deleteProject)
	return project
}

func resolveProject(ctx context.Context, api *client.Client, ref string) (client.Project, error) {
	got, err := api.GetProject(ctx, client.GetProjectParams{ProjectId: ref})
	if err != nil {
		return client.Project{}, apiCallError(err)
	}
	switch found := got.(type) {
	case *client.ProjectHeaders:
		return found.Response, nil
	case *client.ErrorEnvelope:
		if found.Error.Code != "not_found" {
			return client.Project{}, responseError(found)
		}
	default:
		return client.Project{}, responseError(got)
	}
	var cursor string
	for {
		params := client.ListProjectsParams{Limit: client.NewOptInt(100)}
		if cursor != "" {
			params.Cursor = client.NewOptString(cursor)
		}
		pageResult, err := api.ListProjects(ctx, params)
		if err != nil {
			return client.Project{}, apiCallError(err)
		}
		page, ok := pageResult.(*client.ProjectPage)
		if !ok {
			return client.Project{}, responseError(pageResult)
		}
		var match *client.Project
		for index := range page.Items {
			if page.Items[index].Name != ref {
				continue
			}
			if match != nil {
				return client.Project{}, fmt.Errorf("more than one project is named %q; pass the project id", ref)
			}
			match = &page.Items[index]
		}
		if match != nil {
			return *match, nil
		}
		next, ok := page.NextCursor.Get()
		if !ok || next == "" {
			break
		}
		cursor = next
	}
	return client.Project{}, fmt.Errorf("project %q was not found", ref)
}

func countProjectDependents(ctx context.Context, api *client.Client, projectID string) (int, int, error) {
	capsules := 0
	threads := 0
	var capsuleCursor string
	for {
		params := client.ListCapsulesParams{ProjectId: projectID, Limit: client.NewOptInt(100)}
		if capsuleCursor != "" {
			params.Cursor = client.NewOptString(capsuleCursor)
		}
		pageResult, err := api.ListCapsules(ctx, params)
		if err != nil {
			return 0, 0, apiCallError(err)
		}
		page, ok := pageResult.(*client.CapsulePage)
		if !ok {
			return 0, 0, responseError(pageResult)
		}
		for _, capsule := range page.Items {
			if capsule.State == client.CapsuleStateDeleted {
				continue
			}
			capsules++
			count, err := countThreads(ctx, api, capsule.ID)
			if err != nil {
				return 0, 0, err
			}
			threads += count
		}
		next, ok := page.NextCursor.Get()
		if !ok || next == "" {
			return capsules, threads, nil
		}
		capsuleCursor = next
	}
}

func countThreads(ctx context.Context, api *client.Client, capsuleID string) (int, error) {
	total := 0
	var cursor string
	for {
		params := client.ListThreadsParams{CapsuleId: capsuleID, Limit: client.NewOptInt(100)}
		if cursor != "" {
			params.Cursor = client.NewOptString(cursor)
		}
		pageResult, err := api.ListThreads(ctx, params)
		if err != nil {
			return 0, apiCallError(err)
		}
		page, ok := pageResult.(*client.ThreadPage)
		if !ok {
			return 0, responseError(pageResult)
		}
		total += len(page.Items)
		next, ok := page.NextCursor.Get()
		if !ok || next == "" {
			return total, nil
		}
		cursor = next
	}
}

func projectSecretCount(project client.Project) int {
	names := map[string]struct{}{}
	add := func(value client.OptString) {
		name, ok := value.Get()
		if ok && name != "" {
			names[name] = struct{}{}
		}
	}
	add(project.GitSecretName)
	add(project.GitPushSecretName)
	add(project.GithubAPISecretName)
	for _, name := range project.HarnessSecretNames {
		if name != "" {
			names[name] = struct{}{}
		}
	}
	return len(names)
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
		return writeJSON(c.stdout, &secret)
	}
	_, err := fmt.Fprintf(c.stdout, "%s\t%s\tpurpose=%s\tversion=%d\n",
		secret.ID, secret.Name, secret.Purpose, secret.ResourceVersion)
	return err
}

func newCapsuleCommand(config *cliConfig) *cobra.Command {
	capsule := &cobra.Command{Use: "capsule", Short: "Manage Capsules"}

	var createKey, createHarness, createSetup string
	create := &cobra.Command{
		Use:   "create PROJECT_ID [NAME]",
		Short: "Request Capsule creation",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			key, err := idempotencyKey(createKey)
			if err != nil {
				return err
			}
			nameHash := sha256.Sum256([]byte(key))
			name := fmt.Sprintf("capsule-%x", nameHash[:4])
			if len(args) > 1 {
				name = args[1]
			}
			input := &client.CreateCapsuleRequest{Name: name}
			if createSetup != "" {
				input.Setup = client.NewOptString(createSetup)
			}
			if createSetup == "" {
				if err := offerHarnessSetupUpdate(command.Context(), config, createHarness); err != nil {
					return err
				}
			}
			if createHarness != "" {
				input.Harness = client.NewOptString(createHarness)
			}
			result, err := api.CreateCapsule(
				command.Context(),
				input,
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
	create.Flags().StringVar(&createSetup, "setup", "", "saved setup ID, or clean to omit personal configuration")
	create.Flags().StringVar(&createKey, "idempotency-key", "", "mutation replay key (generated if omitted)")
	create.Flags().StringVar(&createHarness, "harness", "", "Project-allowlisted native harness to launch")

	var listCursor, listRole string
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
			switch listRole {
			case "":
			case string(client.CapsuleRoleMain), string(client.CapsuleRoleSub):
				params.Role = client.NewOptCapsuleRole(client.CapsuleRole(listRole))
			default:
				return errors.New("--role must be main or sub")
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
	list.Flags().StringVar(&listRole, "role", "", "only main Capsules or only subagent (sub) Capsules")

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

	capsule.AddCommand(create, list, get, newCapsulePromoteCommand(config))
	capsule.AddCommand(newLifecycleCommand(config, "pause"), newLifecycleCommand(config, "resume"))
	capsule.AddCommand(newLifecycleCommand(config, "delete"), newLifecycleCommand(config, "retry"))
	capsule.AddCommand(newCapsuleGitCommand(config, false), newCapsuleGitCommand(config, true))
	capsule.AddCommand(
		newCapsuleFilesCommand(config), newCapsuleSyncCommand(config), newCapsuleShipCommand(config),
		newCapsuleApproveDeliveryCommand(config), newCapsuleRejectDeliveryCommand(config),
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
			case "retry":
				result, err = api.RetryCapsule(command.Context(), request, client.RetryCapsuleParams{CapsuleId: args[0], IdempotencyKey: key})
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

// defaultAPITokenFile is MERIDIAN_TOKEN_FILE, else the member token
// meridian login saved, else the local daemon's installation token.
func defaultAPITokenFile() string {
	if path := os.Getenv("MERIDIAN_TOKEN_FILE"); path != "" {
		return path
	}
	if member := defaultMemberTokenFile(); fileExists(member) {
		return member
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
		return writeJSON(c.stdout, &project)
	}
	line := fmt.Sprintf("%s\t%s\tversion=%d", project.ID, project.Name, project.ResourceVersion)
	if state, ok := project.State.Get(); ok && state != "" && state != client.ProjectStateActive {
		line += "\tstate=" + string(state)
		if failure, ok := project.Failure.Get(); ok && failure != "" {
			line += "\tfailure=" + failure
		}
	}
	_, err := fmt.Fprintf(c.stdout, "%s\n", line)
	return err
}

func (c *cliConfig) writeCapsule(capsule client.Capsule) error {
	if c.json {
		return writeJSON(c.stdout, &capsule)
	}
	preparation := ""
	if p, ok := capsule.Preparation.Get(); ok {
		preparation = fmt.Sprintf("\tpreparation=%s\treused=%t", p.Stage, p.Reused)
	}
	if role, ok := capsule.Role.Get(); ok && (role == client.CapsuleRoleSub || capsule.PromotedAt.Set) {
		preparation += "\trole=" + string(role)
		if capsule.PromotedAt.Set {
			preparation += "\tpromoted"
		}
	}
	if lead, ok := capsule.LeadThreadId.Get(); ok {
		preparation += "\tlead=" + lead
	}
	_, err := fmt.Fprintf(
		c.stdout, "%s\t%s\tstate=%s\tdesired=%s\tversion=%d%s\n",
		capsule.ID, capsule.Name, capsule.State, capsule.DesiredState, capsule.ResourceVersion, preparation,
	)
	return err
}

func (c *cliConfig) writeProjectPage(page client.ProjectPage) error {
	if c.json {
		return writeJSON(c.stdout, &page)
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
		return writeJSON(c.stdout, &page)
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

// writeJSON takes a pointer so encoding/json reaches the generated client's
// pointer-receiver MarshalJSON methods. Encoding a generated struct by value
// falls back to reflection, where an unset Opt field marshals to no bytes and
// fails the whole document.
func writeJSON[T any](writer io.Writer, value *T) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

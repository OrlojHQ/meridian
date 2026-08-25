// Package harness parses strict, versioned harness configuration inside a Capsule.
package harness

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

const (
	ConfigPath       = "/workspace/.meridian/project.yaml"
	CurrentVersion   = "v1"
	AdapterVersion   = "meridian.adapter.v1"
	maxConfigBytes   = 256 << 10
	maxProfiles      = 64
	maxArguments     = 128
	maxArgumentBytes = 4096
	maxNameBytes     = 128
	maxTimeout       = 24 * time.Hour
)

type InteractionMode string

const (
	InteractionNative     InteractionMode = "native"
	InteractionStructured InteractionMode = "structured"
)

type PromptMode string

const (
	PromptArgument    PromptMode = "argument"
	PromptStdin       PromptMode = "stdin"
	PromptInteractive PromptMode = "interactive"
)

type OutputMode string

const (
	OutputText  OutputMode = "text"
	OutputJSONL OutputMode = "jsonl"
)

type Config struct {
	Version   string    `yaml:"version"`
	Harnesses []Profile `yaml:"harnesses"`
}

type Profile struct {
	Name        string          `yaml:"name"`
	Interaction InteractionMode `yaml:"interactionMode,omitempty"`
	Executable  string          `yaml:"executable,omitempty"`
	Arguments   []string        `yaml:"arguments,omitempty"`
	Adapter     *Adapter        `yaml:"adapter,omitempty"`
	Workdir     string          `yaml:"workingDirectory,omitempty"`
	Prompt      PromptMode      `yaml:"promptMode,omitempty"`
	PTY         bool            `yaml:"pty"`
	Output      OutputMode      `yaml:"outputMode,omitempty"`
	Timeout     time.Duration   `yaml:"-"`
	TimeoutRaw  string          `yaml:"timeout"`
	SecretRefs  []string        `yaml:"secretReferences,omitempty"`
}

type Adapter struct {
	Protocol   string   `yaml:"protocol"`
	Executable string   `yaml:"executable"`
	Arguments  []string `yaml:"arguments,omitempty"`
}

// Parse decodes strict YAML. It is called only by capsuled for trusted image
// manifests and repository configuration; the host control plane never parses either.
func Parse(reader io.Reader) (Config, error) {
	limited := io.LimitReader(reader, maxConfigBytes+1)
	value, err := io.ReadAll(limited)
	if err != nil {
		return Config{}, fmt.Errorf("read harness configuration: %w", err)
	}
	if len(value) > maxConfigBytes {
		return Config{}, errors.New("harness configuration exceeds size limit")
	}
	var config Config
	if err := yaml.UnmarshalWithOptions(value, &config, yaml.Strict()); err != nil {
		return Config{}, fmt.Errorf("decode harness configuration: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c *Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported harness configuration version %q", c.Version)
	}
	if len(c.Harnesses) == 0 || len(c.Harnesses) > maxProfiles {
		return fmt.Errorf("harness profile count must be between 1 and %d", maxProfiles)
	}
	names := make(map[string]struct{}, len(c.Harnesses))
	for index := range c.Harnesses {
		profile := &c.Harnesses[index]
		if err := profile.validate(); err != nil {
			return fmt.Errorf("harness profile %d: %w", index, err)
		}
		if _, exists := names[profile.Name]; exists {
			return fmt.Errorf("duplicate harness profile name %q", profile.Name)
		}
		names[profile.Name] = struct{}{}
	}
	return nil
}

func (c Config) Profile(name string) (Profile, error) {
	for _, profile := range c.Harnesses {
		if profile.Name == name {
			return profile, nil
		}
	}
	return Profile{}, fmt.Errorf("harness profile %q not found", name)
}

func (p *Profile) validate() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > maxNameBytes || strings.ContainsAny(p.Name, "\x00\r\n/\\") {
		return errors.New("name is invalid")
	}
	if p.Interaction == "" {
		p.Interaction = InteractionNative
	}
	switch p.Interaction {
	case InteractionNative:
		if p.Adapter != nil {
			return errors.New("native profile cannot define an adapter")
		}
		p.Executable = strings.TrimSpace(p.Executable)
		if err := validateExecutable(p.Executable); err != nil {
			return err
		}
		if err := validateArguments(p.Arguments, false); err != nil {
			return err
		}
	case InteractionStructured:
		if p.PTY {
			return errors.New("structured interaction mode cannot use a PTY")
		}
		if p.Executable != "" || len(p.Arguments) != 0 ||
			p.Prompt != "" || p.Output != "" {
			return errors.New("structured profile cannot define native executable, prompt, or output fields")
		}
		if p.Adapter == nil || p.Adapter.Protocol != AdapterVersion {
			return fmt.Errorf("structured profile requires adapter protocol %q", AdapterVersion)
		}
		p.Adapter.Executable = strings.TrimSpace(p.Adapter.Executable)
		if err := validateExecutable(p.Adapter.Executable); err != nil {
			return fmt.Errorf("adapter %w", err)
		}
		if err := validateArguments(p.Adapter.Arguments, true); err != nil {
			return fmt.Errorf("adapter %w", err)
		}
	default:
		return fmt.Errorf("invalid interaction mode %q", p.Interaction)
	}
	if p.Workdir == "" {
		p.Workdir = "."
	}
	if filepath.IsAbs(p.Workdir) || filepath.Clean(p.Workdir) != p.Workdir ||
		p.Workdir == ".." || strings.HasPrefix(p.Workdir, ".."+string(filepath.Separator)) ||
		strings.ContainsRune(p.Workdir, '\x00') {
		return errors.New("working directory must be a clean relative path beneath /workspace")
	}
	if p.Interaction == InteractionNative {
		switch p.Prompt {
		case PromptArgument, PromptStdin, PromptInteractive:
		default:
			return fmt.Errorf("invalid prompt mode %q", p.Prompt)
		}
		switch p.Output {
		case OutputText, OutputJSONL:
		default:
			return fmt.Errorf("invalid output mode %q", p.Output)
		}
		if p.Prompt == PromptInteractive && !p.PTY {
			return errors.New("interactive prompt mode requires a PTY")
		}
	}
	timeout, err := time.ParseDuration(p.TimeoutRaw)
	if err != nil || timeout <= 0 || timeout > maxTimeout {
		return fmt.Errorf("timeout must be greater than zero and at most %s", maxTimeout)
	}
	p.Timeout = timeout
	if len(p.SecretRefs) > 64 {
		return errors.New("too many secret references")
	}
	seen := make(map[string]struct{}, len(p.SecretRefs))
	for _, reference := range p.SecretRefs {
		if reference == "" || len(reference) > 256 || strings.ContainsAny(reference, "\x00\r\n=:/\\") {
			return errors.New("secret references must be opaque metadata names, not inline values")
		}
		if _, exists := seen[reference]; exists {
			return fmt.Errorf("duplicate secret reference %q", reference)
		}
		seen[reference] = struct{}{}
	}
	return nil
}

func validateExecutable(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxArgumentBytes ||
		strings.ContainsAny(value, "\x00\r\n") || strings.HasPrefix(value, "-") {
		return errors.New("executable is invalid")
	}
	return nil
}

func validateArguments(arguments []string, rejectInlineSecrets bool) error {
	if len(arguments) > maxArguments {
		return fmt.Errorf("argument count exceeds %d", maxArguments)
	}
	for _, argument := range arguments {
		if len(argument) > maxArgumentBytes || strings.ContainsRune(argument, '\x00') {
			return errors.New("argument is invalid")
		}
		if rejectInlineSecrets && looksLikeInlineSecret(argument) {
			return errors.New("arguments cannot contain inline secret values")
		}
	}
	return nil
}

func looksLikeInlineSecret(argument string) bool {
	lower := strings.ToLower(argument)
	for _, marker := range []string{
		"token=", "password=", "passwd=", "secret=", "api_key=", "apikey=", "api-key=",
		"authorization=", "bearer ",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

package harness

import (
	"strings"
	"testing"
	"time"
)

func TestParseStrictValidConfiguration(t *testing.T) {
	config, err := Parse(strings.NewReader(`
version: v1
harnesses:
  - name: mock
    executable: /usr/local/bin/mock-harness
    arguments: ["--mode", "edit"]
    workingDirectory: fixture
    promptMode: stdin
    pty: false
    outputMode: jsonl
    timeout: 30s
`))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.Profile("mock")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Timeout != 30*time.Second || profile.Workdir != "fixture" ||
		len(profile.Arguments) != 2 || profile.Interaction != InteractionNative {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestParseStructuredConfiguration(t *testing.T) {
	config, err := Parse(strings.NewReader(`
version: v1
harnesses:
  - name: structured
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/example-adapter
      arguments: ["--driver", "example"]
    workingDirectory: fixture
    pty: false
    timeout: 30m
`))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.Profile("structured")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Interaction != InteractionStructured || profile.PTY ||
		profile.Adapter == nil || profile.Adapter.Protocol != AdapterVersion ||
		len(profile.Adapter.Arguments) != 2 {
		t.Fatalf("structured profile = %#v", profile)
	}
}

func TestParseRejectsMaliciousAndMalformedConfiguration(t *testing.T) {
	tests := map[string]string{
		"unknown field": `
version: v1
harnesses:
  - name: mock
    executable: mock
    workingDirectory: .
    promptMode: stdin
    pty: false
    outputMode: text
    timeout: 1s
    command: "sh -c evil"
`,
		"duplicate names": `
version: v1
harnesses:
  - &base {name: mock, executable: mock, workingDirectory: ., promptMode: stdin, pty: false, outputMode: text, timeout: 1s}
  - *base
`,
		"absolute workdir": `
version: v1
harnesses:
  - {name: mock, executable: mock, workingDirectory: /tmp, promptMode: stdin, pty: false, outputMode: text, timeout: 1s}
`,
		"escaping workdir": `
version: v1
harnesses:
  - {name: mock, executable: mock, workingDirectory: ../host, promptMode: stdin, pty: false, outputMode: text, timeout: 1s}
`,
		"inline secret": `
version: v1
harnesses:
  - {name: mock, executable: mock, workingDirectory: ., promptMode: stdin, pty: false, outputMode: text, timeout: 1s, secretReferences: ["TOKEN=plaintext"]}
`,
		"invalid mode": `
version: v1
harnesses:
  - {name: mock, executable: mock, workingDirectory: ., promptMode: shell, pty: false, outputMode: text, timeout: 1s}
`,
		"empty executable": `
version: v1
harnesses:
  - {name: mock, executable: "", workingDirectory: ., promptMode: stdin, pty: false, outputMode: text, timeout: 1s}
`,
		"structured PTY": `
version: v1
harnesses:
  - {name: mock, interactionMode: structured, adapter: {protocol: meridian.adapter.v1, executable: adapter}, workingDirectory: ., pty: true, timeout: 1s}
`,
		"structured native fields": `
version: v1
harnesses:
  - {name: mock, interactionMode: structured, executable: harness, adapter: {protocol: meridian.adapter.v1, executable: adapter}, workingDirectory: ., pty: false, timeout: 1s}
`,
		"structured wrong protocol": `
version: v1
harnesses:
  - {name: mock, interactionMode: structured, adapter: {protocol: meridian.adapter.v2, executable: adapter}, workingDirectory: ., pty: false, timeout: 1s}
`,
		"structured inline secret": `
version: v1
harnesses:
  - {name: mock, interactionMode: structured, adapter: {protocol: meridian.adapter.v1, executable: adapter, arguments: ["--token=plaintext"]}, workingDirectory: ., pty: false, timeout: 1s}
`,
		"native adapter": `
version: v1
harnesses:
  - {name: mock, interactionMode: native, executable: harness, adapter: {protocol: meridian.adapter.v1, executable: adapter}, workingDirectory: ., promptMode: stdin, pty: false, outputMode: text, timeout: 1s}
`,
		"malformed": "version: [",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

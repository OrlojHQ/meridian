// mock-harness is a deterministic, credential-free integration harness.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	output := flag.String("output", "text", "text or jsonl")
	delay := flag.Duration("delay", 0, "deterministic delay for cancellation tests")
	interactive := flag.Bool("interactive", false, "read one interactive task line")
	flag.Parse()
	task := strings.Join(flag.Args(), " ")
	if task == "" {
		var value []byte
		var err error
		if *interactive {
			value, err = bufio.NewReader(io.LimitReader(os.Stdin, 256<<10)).ReadBytes('\n')
		} else {
			value, err = io.ReadAll(io.LimitReader(os.Stdin, 256<<10))
		}
		if err != nil {
			fail(*output, "read_task")
		}
		task = strings.TrimSpace(string(value))
	}
	emit(*output, "started", map[string]any{"version": 1})
	if *delay > 0 || task == "long" {
		duration := *delay
		if duration == 0 {
			duration = 30 * time.Second
		}
		time.Sleep(duration)
	}
	if err := os.WriteFile("fixture.txt", []byte("mock edit complete\n"), 0o600); err != nil {
		fail(*output, "edit_failed")
	}
	emit(*output, "edited", map[string]any{"path": "fixture.txt"})
	command := exec.Command("/usr/local/bin/fixture-test")
	command.Dir = "."
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		fail(*output, "fixture_test_failed")
	}
	emit(*output, "tested", map[string]any{"passed": true})
	emit(*output, "completed", map[string]any{"exitStatus": 0})
}

func emit(mode, event string, metadata map[string]any) {
	if mode == "jsonl" {
		value := map[string]any{"event": event, "metadata": metadata}
		_ = json.NewEncoder(os.Stdout).Encode(value)
		return
	}
	writer := bufio.NewWriter(os.Stdout)
	_, _ = fmt.Fprintln(writer, event)
	_ = writer.Flush()
}

func fail(mode, reason string) {
	emit(mode, "failed", map[string]any{"reason": reason})
	os.Exit(1)
}

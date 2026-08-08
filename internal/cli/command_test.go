package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func invokeCommand(ctx context.Context, args ...string) (string, string, error) {
	var output, errorOutput bytes.Buffer
	err := RunContext(ctx, args, &output, &errorOutput)
	return output.String(), errorOutput.String(), err
}

func runOutsideWorkspace(t *testing.T) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func TestCommandRootHelp(t *testing.T) {
	commands := []string{"login", "doctor", "clone", "status", "add", "reset", "diff", "commit", "uncommit", "log", "pull", "merge", "migrate", "push", "release", "version", "completion"}
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		output, errorOutput, err := invokeCommand(context.Background(), args...)
		if err != nil {
			t.Fatalf("RunContext(%q) error = %v", args, err)
		}
		for _, command := range commands {
			if !strings.Contains(output, command) {
				t.Errorf("RunContext(%q) help omits %q", args, command)
			}
		}
		if errorOutput != "" {
			t.Errorf("RunContext(%q) error output = %q", args, errorOutput)
		}
	}
}

func TestCommandHelpForEveryCommand(t *testing.T) {
	commands := []string{"login", "doctor", "clone", "status", "add", "reset", "diff", "commit", "uncommit", "log", "pull", "merge", "migrate", "push", "release", "version", "completion"}
	for _, name := range commands {
		t.Run(name, func(t *testing.T) {
			for _, args := range [][]string{{"help", name}, {name, "--help"}, {name, "-h"}} {
				output, errorOutput, err := invokeCommand(context.Background(), args...)
				if err != nil {
					t.Fatalf("RunContext(%q) error = %v", args, err)
				}
				if output == "" || !strings.Contains(output, name) {
					t.Errorf("RunContext(%q) output = %q", args, output)
				}
				if errorOutput != "" {
					t.Errorf("RunContext(%q) error output = %q", args, errorOutput)
				}
			}
		})
	}

	for _, args := range [][]string{{"help", "status"}, {"status", "--help"}, {"status", "-h"}} {
		output, _, _ := invokeCommand(context.Background(), args...)
		if !strings.Contains(output, "--json") {
			t.Errorf("RunContext(%q) help omits --json: %q", args, output)
		}
	}
}

func TestCommandVersionAliases(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		output, errorOutput, err := invokeCommand(context.Background(), args...)
		if err != nil {
			t.Fatalf("RunContext(%q) error = %v", args, err)
		}
		if output != "gew 0.6.0\n" {
			t.Errorf("RunContext(%q) output = %q", args, output)
		}
		if errorOutput != "" {
			t.Errorf("RunContext(%q) error output = %q", args, errorOutput)
		}
	}
	if _, _, err := invokeCommand(context.Background(), "version", "extra"); err == nil {
		t.Fatal("version accepted an extra operand")
	}
}

func TestCommandArityAndValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown command", args: []string{"unknown"}, want: `unknown command "unknown"; run 'gew help'`},
		{name: "login missing URL", args: []string{"login"}, want: "expected 1 argument"},
		{name: "clone extra operand", args: []string{"clone", "one", "two", "three"}, want: "expected between 1 and 2 arguments"},
		{name: "doctor extra operand", args: []string{"doctor", "extra"}, want: "expected 0 argument"},
		{name: "status extra operand", args: []string{"status", "extra"}, want: "expected 0 argument"},
		{name: "commit missing message", args: []string{"commit"}, want: "Required flag"},
		{name: "commit blank message", args: []string{"commit", "-m", "  \t "}, want: "must not be blank"},
		{name: "merge missing mode", args: []string{"merge"}, want: "one of these flags needs to be provided"},
		{name: "merge conflicting modes", args: []string{"merge", "--abort", "--continue"}, want: "cannot be set along with"},
		{name: "merge abort message", args: []string{"merge", "--abort", "-m", "ignored"}, want: "valid only with --continue"},
		{name: "migrate missing target", args: []string{"migrate"}, want: "Required flag"},
		{name: "migrate wrong target", args: []string{"migrate", "--to", "svn"}, want: "unsupported migration target"},
		{name: "unknown flag", args: []string{"status", "--unknown"}, want: "flag provided but not defined"},
		{name: "invalid request timeout", args: []string{"login", "--token", "test", "--request-timeout", "500ms", "https://example.test"}, want: "request timeout must be between"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, errorOutput, err := invokeCommand(context.Background(), test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RunContext(%q) error = %v, want substring %q", test.args, err, test.want)
			}
			if strings.Contains(err.Error(), "gew:") {
				t.Errorf("library error owns executable prefix: %v", err)
			}
			if output != "" || errorOutput != "" {
				t.Errorf("RunContext(%q) wrote output=%q errorOutput=%q", test.args, output, errorOutput)
			}
		})
	}
}

func TestCommandFlexibleFlagPlacementAndAliases(t *testing.T) {
	runOutsideWorkspace(t)
	_, _, err := invokeCommand(context.Background(), "clone", "acme/demo", "--backend", "invalid")
	if err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("flag after positional was not parsed: %v", err)
	}

	for _, args := range [][]string{{"add", "-A"}, {"add", "--all"}, {"add", "--", "-leading-path"}} {
		_, _, err := invokeCommand(context.Background(), args...)
		if err == nil || !strings.Contains(err.Error(), "not inside a gew workspace") {
			t.Fatalf("RunContext(%q) error = %v; flag alias or -- handling failed", args, err)
		}
	}
}

func TestCommandGraphDoesNotRetainFlagValues(t *testing.T) {
	runOutsideWorkspace(t)
	for _, args := range [][]string{{"status", "--json"}, {"status"}} {
		_, _, err := invokeCommand(context.Background(), args...)
		if err == nil || !strings.Contains(err.Error(), "not inside a gew workspace") {
			t.Fatalf("RunContext(%q) error = %v", args, err)
		}
	}
}

func TestCommandCompletionAndTokenRedaction(t *testing.T) {
	t.Setenv("GEW_TOKEN", "command-test-token-must-not-leak")
	for _, shell := range []string{"bash", "zsh", "fish", "pwsh"} {
		output, errorOutput, err := invokeCommand(context.Background(), "completion", shell)
		if err != nil {
			t.Fatalf("completion %s error = %v", shell, err)
		}
		if output == "" {
			t.Errorf("completion %s produced no output", shell)
		}
		if errorOutput != "" || strings.Contains(output+errorOutput, "command-test-token-must-not-leak") {
			t.Errorf("completion %s leaked token or wrote diagnostics", shell)
		}
	}
	output, errorOutput, err := invokeCommand(context.Background(), "login", "--help")
	if err != nil || !strings.Contains(output, "GEW_TOKEN") || !strings.Contains(output, "GEW_HTTP_TIMEOUT") || strings.Contains(output+errorOutput, "command-test-token-must-not-leak") {
		t.Fatalf("login help redaction failed: output=%q errorOutput=%q err=%v", output, errorOutput, err)
	}
}

func TestCommandCancellationReachesRemoteRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	t.Setenv("GEW_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, err := invokeCommand(ctx, "login", "--token", "test-token", server.URL)
		result <- err
	}()

	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("remote request did not start")
	}

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled command error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled command did not return promptly")
	}
}

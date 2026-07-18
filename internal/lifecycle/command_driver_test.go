package lifecycle

import (
	"context"
	"testing"
)

type commandCall struct {
	command Command
	env     []string
}

type commandRunner struct{ calls []commandCall }

func (r *commandRunner) Run(_ context.Context, command Command, environment []string) error {
	r.calls = append(r.calls, commandCall{command: command, env: append([]string(nil), environment...)})
	return nil
}

func TestCommandDriverPassesFenceAndManifestContext(t *testing.T) {
	commands := CommandSet{
		Inspect: Command{Name: "inspect"}, StartPersistence: Command{Name: "start-persistence"},
		RunMigration: Command{Name: "migration"}, StartWorkers: Command{Name: "start-workers"},
		StartServers: Command{Name: "start-servers"}, DrainServers: Command{Name: "drain-servers"},
		StopWorkers: Command{Name: "stop-workers"}, StopPersistence: Command{Name: "stop-persistence"},
		ReleaseActiveStorage: Command{Name: "release-storage"},
	}
	runner := &commandRunner{}
	driver, err := NewCommandDriver(runner, commands)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.StartPersistence(context.Background(), 42, Manifest{Backend: "sqlite", Artifact: "artifacts/42.bin", SchemaVersion: 7}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || runner.calls[0].command.Name != "start-persistence" {
		t.Fatalf("calls=%+v", runner.calls)
	}
	environment := runner.calls[0].env
	for _, expected := range []string{"SAMEOLDCHAT_LIFECYCLE_GENERATION=42", "SAMEOLDCHAT_BACKEND=sqlite", "SAMEOLDCHAT_SNAPSHOT_ARTIFACT=artifacts/42.bin", "SAMEOLDCHAT_SCHEMA_VERSION=7"} {
		found := false
		for _, value := range environment {
			if value == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("environment=%v missing %q", environment, expected)
		}
	}
}

func TestCommandDriverRejectsIncompleteConfiguration(t *testing.T) {
	if _, err := NewCommandDriver(&commandRunner{}, CommandSet{}); err == nil {
		t.Fatal("incomplete command configuration was accepted")
	}
}

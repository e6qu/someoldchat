package lifecycle

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Command struct {
	Name string
	Args []string
}

type CommandSet struct {
	Inspect              Command
	StartPersistence     Command
	RunMigration         Command
	StartWorkers         Command
	StartServers         Command
	DrainServers         Command
	StopWorkers          Command
	StopPersistence      Command
	ReleaseActiveStorage Command
}

type CommandRunner interface {
	Run(context.Context, Command, []string) error
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, command Command, environment []string) error {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	process.Env = append(os.Environ(), environment...)
	return process.Run()
}

type CommandDriver struct {
	runner   CommandRunner
	commands CommandSet
}

func NewCommandDriver(runner CommandRunner, commands CommandSet) (CommandDriver, error) {
	if runner == nil {
		return CommandDriver{}, errors.New("command lifecycle driver requires a runner")
	}
	for name, command := range map[string]Command{
		"inspect":                commands.Inspect,
		"start persistence":      commands.StartPersistence,
		"run migration":          commands.RunMigration,
		"start workers":          commands.StartWorkers,
		"start servers":          commands.StartServers,
		"drain servers":          commands.DrainServers,
		"stop workers":           commands.StopWorkers,
		"stop persistence":       commands.StopPersistence,
		"release active storage": commands.ReleaseActiveStorage,
	} {
		if strings.TrimSpace(command.Name) == "" {
			return CommandDriver{}, errors.New("command lifecycle driver requires " + name + " command")
		}
	}
	return CommandDriver{runner: runner, commands: commands}, nil
}

func (d CommandDriver) Inspect(ctx context.Context, fence uint64) error {
	return d.run(ctx, d.commands.Inspect, fence)
}

func (d CommandDriver) StartPersistence(ctx context.Context, fence uint64, manifest Manifest) error {
	environment := lifecycleEnvironment(fence)
	environment = append(environment, "SAMEOLDCHAT_BACKEND="+manifest.Backend, "SAMEOLDCHAT_SNAPSHOT_ARTIFACT="+manifest.Artifact, "SAMEOLDCHAT_SCHEMA_VERSION="+strconv.Itoa(manifest.SchemaVersion))
	return d.runner.Run(ctx, d.commands.StartPersistence, environment)
}

func (d CommandDriver) RunMigration(ctx context.Context, fence uint64, schemaVersion int) error {
	environment := lifecycleEnvironment(fence)
	environment = append(environment, "SAMEOLDCHAT_SCHEMA_VERSION="+strconv.Itoa(schemaVersion))
	return d.runner.Run(ctx, d.commands.RunMigration, environment)
}

func (d CommandDriver) StartWorkers(ctx context.Context, fence uint64) error {
	return d.run(ctx, d.commands.StartWorkers, fence)
}

func (d CommandDriver) StartServers(ctx context.Context, fence uint64) error {
	return d.run(ctx, d.commands.StartServers, fence)
}

func (d CommandDriver) DrainServers(ctx context.Context, fence uint64) error {
	return d.run(ctx, d.commands.DrainServers, fence)
}

func (d CommandDriver) StopWorkers(ctx context.Context, fence uint64) error {
	return d.run(ctx, d.commands.StopWorkers, fence)
}

func (d CommandDriver) StopPersistence(ctx context.Context, fence uint64) error {
	return d.run(ctx, d.commands.StopPersistence, fence)
}

func (d CommandDriver) ReleaseActiveStorage(ctx context.Context, fence uint64) error {
	return d.run(ctx, d.commands.ReleaseActiveStorage, fence)
}

func (d CommandDriver) run(ctx context.Context, command Command, fence uint64) error {
	return d.runner.Run(ctx, command, lifecycleEnvironment(fence))
}

func lifecycleEnvironment(fence uint64) []string {
	return []string{"SAMEOLDCHAT_LIFECYCLE_GENERATION=" + strconv.FormatUint(fence, 10)}
}

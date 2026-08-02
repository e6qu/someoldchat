// Package processfault qualifies the crash-only requirement against a real
// process.
//
// specs/persistence.md requires that components "MUST be safe to terminate
// abruptly and restart through the normal startup/recovery path", and
// docs/architecture.md lists process faults among the crash/restart tests that
// "run during normal verification". Nothing in the repository killed a process:
// every restart test either reopened a store handle or expired a lease on a
// live one, so the startup path — schema migration against an existing
// database, credential seeding against already-seeded rows, recovery of state
// written by a process that never got to shut down — was exercised only by
// deployment.
//
// This runs the real binary, kills it with SIGKILL so no shutdown handler can
// run, and starts it again on the same database.
package processfault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	// The identities cmd/server seeds into an empty database. Using them keeps
	// this test on the ordinary startup path rather than a seeded-by-the-test
	// shortcut that would skip the part under examination.
	qualificationWorkspace = "Tdev"
	qualificationUser      = "Udev"
	qualificationChannel   = "Cdev"
	qualificationAPIToken  = "xoxb-process-fault"
	// Durable local storage requires a credential key, because application
	// signing credentials are encrypted at rest. It is fixed rather than random
	// so the second boot decrypts what the first one wrote.
	qualificationCredentialKey = "6f0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1"
	qualificationBootWindow    = 30 * time.Second
)

// server is one running instance of the binary, addressed over HTTP.
type server struct {
	binary  string
	dbPath  string
	address string
	command *exec.Cmd
	exited  chan error
}

func newServer(t *testing.T) *server {
	t.Helper()
	value := &server{
		binary:  buildServerBinary(t),
		dbPath:  filepath.Join(t.TempDir(), "process-fault.db"),
		address: reservePort(t),
	}
	t.Cleanup(func() {
		if value.command != nil {
			_ = value.command.Process.Kill()
			<-value.exited
			value.command = nil
		}
	})
	return value
}

// start launches the binary and waits until it reports healthy. It is called
// once before the crash and once after, with identical arguments, because
// "restart through the normal startup/recovery path" means the same command
// line — not a recovery mode.
func (s *server) start(t *testing.T) {
	t.Helper()
	command := exec.Command(s.binary,
		"-chat-mode", "local",
		"-store", "sqlite",
		"-db", s.dbPath,
		"-addr", s.address,
		"-api-token", qualificationAPIToken,
		"-api-rate-limit=false",
		"-app-credential-key-hex", qualificationCredentialKey,
	)
	output := &strings.Builder{}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	s.command = command

	// Waiting in the background turns "the server rejected its configuration"
	// into an immediate, legible failure instead of a thirty-second timeout that
	// says nothing about why.
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	s.exited = exited

	deadline := time.Now().Add(qualificationBootWindow)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + s.address + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case err := <-exited:
			s.command = nil
			t.Fatalf("the server exited during startup: %v\n%s", err, output.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("the server did not become healthy within %s\n%s", qualificationBootWindow, output.String())
}

// kill terminates the process with SIGKILL. This is the point of the test: the
// process gets no opportunity to flush, checkpoint, close a handle, or run any
// deferred work, so whatever is readable afterwards was durable at the moment
// the write was acknowledged.
func (s *server) kill(t *testing.T) {
	t.Helper()
	if err := s.command.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill the server: %v", err)
	}
	err := <-s.exited
	s.command = nil

	// A process that shut down gracefully would prove nothing, so the test
	// asserts the manner of death rather than assuming it.
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("wait after SIGKILL returned %v, want an ExitError", err)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("the server exited as %v, want termination by SIGKILL", exit)
	}
}

func (s *server) call(t *testing.T, method string, form url.Values) map[string]any {
	t.Helper()
	endpoint := "http://" + s.address + "/api/" + method
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+qualificationAPIToken)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s returned %d: %s", method, response.StatusCode, body)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("%s returned unparseable JSON: %v: %s", method, err, body)
	}
	if decoded["ok"] != true {
		t.Fatalf("%s failed: %s", method, body)
	}
	return decoded
}

// TestCommittedStateSurvivesSIGKILL is the crash-only contract at the process
// boundary: what the API acknowledged before the kill is still there after it.
func TestCommittedStateSurvivesSIGKILL(t *testing.T) {
	instance := newServer(t)
	instance.start(t)

	posted := instance.call(t, "chat.postMessage", url.Values{
		"channel": {qualificationChannel},
		"text":    {"committed before the crash"},
	})
	timestamp, _ := posted["ts"].(string)
	if timestamp == "" {
		t.Fatalf("chat.postMessage returned no ts: %+v", posted)
	}
	// The message row is the easy half. State written through a different table
	// in a different transaction is where a crash actually bites, so the test
	// also pins and reacts to it and checks both back afterwards.
	instance.call(t, "reactions.add", url.Values{
		"channel":   {qualificationChannel},
		"timestamp": {timestamp},
		"name":      {"thumbsup"},
	})
	instance.call(t, "pins.add", url.Values{
		"channel":   {qualificationChannel},
		"timestamp": {timestamp},
	})

	instance.kill(t)
	instance.start(t)

	history := instance.call(t, "conversations.history", url.Values{
		"channel": {qualificationChannel},
		"limit":   {"50"},
	})
	messages, _ := history["messages"].([]any)
	found := false
	for _, entry := range messages {
		message, _ := entry.(map[string]any)
		if message["ts"] == timestamp {
			found = true
			if message["text"] != "committed before the crash" {
				t.Fatalf("the message survived with text %q", message["text"])
			}
		}
	}
	if !found {
		t.Fatalf("the acknowledged message did not survive SIGKILL: %+v", messages)
	}

	reactions := instance.call(t, "reactions.get", url.Values{
		"channel":   {qualificationChannel},
		"timestamp": {timestamp},
	})
	message, _ := reactions["message"].(map[string]any)
	applied, _ := message["reactions"].([]any)
	if len(applied) != 1 {
		t.Fatalf("reactions=%+v, want the reaction to have survived SIGKILL", applied)
	}
	if first, _ := applied[0].(map[string]any); first["name"] != "thumbsup" {
		t.Fatalf("reaction=%+v, want thumbsup", first)
	}

	pins := instance.call(t, "pins.list", url.Values{
		"channel": {qualificationChannel},
	})
	items, _ := pins["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("pins=%+v, want the pin to have survived SIGKILL", items)
	}
}

// TestRestartAcceptsAnAlreadySeededDatabase covers the half of the startup path
// that only a second boot reaches. The first boot migrates an empty database
// and seeds credentials and memberships; the second runs the same code against
// rows that already exist. A migration that is not idempotent, or a seed that
// inserts rather than upserts, fails here and nowhere else — and it would fail
// in production on every restart, not on deployment.
func TestRestartAcceptsAnAlreadySeededDatabase(t *testing.T) {
	instance := newServer(t)
	instance.start(t)
	instance.kill(t)
	instance.start(t)
	instance.kill(t)
	instance.start(t)

	// Still fully functional on the third boot, not merely willing to start.
	instance.call(t, "chat.postMessage", url.Values{
		"channel": {qualificationChannel},
		"text":    {"posted after two crashes"},
	})
}

// buildServerBinary compiles the real cmd/server once per test binary. Building
// it here rather than depending on `make build` keeps the test self-contained
// under a plain `go test ./...`, which is how it reaches `make check`.
func buildServerBinary(t *testing.T) string {
	t.Helper()
	serverBinaryOnce.Do(func() {
		directory, err := os.MkdirTemp("", "sameoldchat-process-fault")
		if err != nil {
			serverBinaryError = err
			return
		}
		binary := filepath.Join(directory, "server")
		build := exec.Command("go", "build", "-o", binary, "./cmd/server")
		build.Dir = repositoryRoot(t)
		if output, err := build.CombinedOutput(); err != nil {
			serverBinaryError = fmt.Errorf("build cmd/server: %w: %s", err, output)
			return
		}
		serverBinaryPath = binary
	})
	if serverBinaryError != nil {
		t.Fatal(serverBinaryError)
	}
	return serverBinaryPath
}

var (
	serverBinaryOnce  sync.Once
	serverBinaryPath  string
	serverBinaryError error
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(working, "..", "..")
}

// reservePort picks a free port and releases it. The server needs the same
// address across a restart, so the port cannot be chosen by the kernel at bind
// time; asking the kernel for a free one and handing it straight over is the
// closest available substitute, and keeps this package safe to run alongside
// the rest of `go test ./...`.
func reservePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

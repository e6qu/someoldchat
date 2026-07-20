package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Body.String(); got != "ok\n" {
		t.Fatalf("body = %q, want %q", got, "ok\\n")
	}
}

func TestApplicationRootRedirectsToTheAuthenticatedApplication(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	applicationRootHandler(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/app" {
		t.Fatalf("root response = %d location=%q", response.Code, response.Header().Get("Location"))
	}
}

func TestReadinessChecksTheSelectedService(t *testing.T) {
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "Tdev"})
	selected.SeedUser(domain.User{ID: "Udev", WorkspaceID: "Tdev"})
	selected.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "Tdev", Name: "general"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", readinessHandler(service.Messages{Store: selected}))
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, request)
	if result.Code != http.StatusOK || result.Body.String() != "ready\n" {
		t.Fatalf("ready status=%d body=%q", result.Code, result.Body.String())
	}
	mux = http.NewServeMux()
	mux.HandleFunc("GET /readyz", readinessHandler(service.Messages{Store: memory.New()}))
	result = httptest.NewRecorder()
	mux.ServeHTTP(result, request)
	if result.Code != http.StatusServiceUnavailable || result.Body.String() != "not ready\n" {
		t.Fatalf("not-ready status=%d body=%q", result.Code, result.Body.String())
	}
}

func TestParseClusterNormalizesAddresses(t *testing.T) {
	got, err := localchat.ParseCluster(" node-a:19001, node-b:19001 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"node-a:19001", "node-b:19001"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cluster = %#v, want %#v", got, want)
	}
}

func TestParseClusterRejectsEmptyAddress(t *testing.T) {
	if _, err := localchat.ParseCluster("node-a:19001,,node-b:19001"); err == nil {
		t.Fatal("empty cluster address was accepted")
	}
}

func TestDatabaseDSNDefaultUsesRuntimeEnvironment(t *testing.T) {
	t.Setenv("SAMEOLDCHAT_DATABASE_URL", "postgres://sameoldchat:secret@postgres.example:5432/sameoldchat?sslmode=require")
	if got, want := databaseDSNDefault(), "postgres://sameoldchat:secret@postgres.example:5432/sameoldchat?sslmode=require"; got != want {
		t.Fatalf("database DSN default = %q, want %q", got, want)
	}
}

func TestResolveDatabaseDSNUsesEnvironmentOnlyForLocalComposition(t *testing.T) {
	t.Setenv("SAMEOLDCHAT_DATABASE_URL", "postgres://sameoldchat:secret@postgres.example/sameoldchat")
	if got, err := resolveDatabaseDSN("local", ""); err != nil || got != "postgres://sameoldchat:secret@postgres.example/sameoldchat" {
		t.Fatalf("local DSN = %q, error=%v", got, err)
	}
	if got, err := resolveDatabaseDSN("grpc", ""); err != nil || got != "" {
		t.Fatalf("distributed DSN = %q, error=%v", got, err)
	}
}

func TestResolveDatabaseDSNRejectsExplicitDistributedLocalStorage(t *testing.T) {
	if _, err := resolveDatabaseDSN("grpc", "file:chat.db"); err == nil || !strings.Contains(err.Error(), "cannot use a local database DSN") {
		t.Fatalf("error=%v, want explicit distributed local-storage rejection", err)
	}
}

func TestResolveDatabaseDSNRejectsUnknownComposition(t *testing.T) {
	if _, err := resolveDatabaseDSN("", ""); err == nil {
		t.Fatal("unknown chat composition was accepted")
	}
}

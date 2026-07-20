//go:build postgres

package web

import (
	"context"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatgrpc "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestOIDCProvisioningAcrossPostgreSQLAndGRPC(t *testing.T) {
	dsn := os.Getenv("SAMEOLDCHAT_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SAMEOLDCHAT_POSTGRES_DSN is required for PostgreSQL identity qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	workspaceID := domain.WorkspaceID("T-identity-" + suffix)
	lookupUserID := domain.UserID("U-identity-admin-" + suffix)
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "Identity qualification"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: lookupUserID, WorkspaceID: workspaceID, Email: "admin-" + suffix + "@example.test", Name: "admin"}); err != nil {
		t.Fatal(err)
	}

	implementation := service.Messages{Store: repository}
	server := grpc.NewServer()
	if err := chatgrpc.RegisterServer(server, implementation, repository, repository, repository); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	connection, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	remote, err := chatgrpc.NewRemote(connection)
	if err != nil {
		t.Fatal(err)
	}
	handler := LoginHandler{service: remote, workspace: workspaceID, lookupUser: lookupUserID}
	identity := externalIdentity{Subject: "sha-auth-subject-" + suffix, Email: "developer-" + suffix + "@example.test", Name: "Remote Developer", Role: "developer"}

	const attempts = 2
	users := make(chan domain.User, attempts)
	errorsFound := make(chan error, attempts)
	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	for range attempts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start.Wait()
			user, resolveErr := handler.resolveIdentityUser(ctx, "oidc", identity)
			if resolveErr != nil {
				errorsFound <- resolveErr
				return
			}
			users <- user
		}()
	}
	start.Done()
	workers.Wait()
	close(users)
	close(errorsFound)
	for resolveErr := range errorsFound {
		t.Fatalf("concurrent remote identity provisioning failed: %v", resolveErr)
	}
	var provisioned domain.UserID
	count := 0
	for user := range users {
		count++
		if provisioned == "" {
			provisioned = user.ID
		}
		if user.ID != provisioned || user.Email != identity.Email {
			t.Fatalf("provisioned user=%+v, want stable user %q and email %q", user, provisioned, identity.Email)
		}
	}
	if count != attempts {
		t.Fatalf("successful provisioning attempts=%d, want %d", count, attempts)
	}
	link, err := remote.GetExternalIdentity(ctx, workspaceID, "oidc", identity.Subject)
	if err != nil {
		t.Fatal(err)
	}
	if link.UserID != provisioned {
		t.Fatalf("external identity=%+v, want user %q", link, provisioned)
	}
	page, err := remote.AdminListUsers(ctx, workspaceID, lookupUserID, domain.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	for _, item := range page.Users {
		if item.User.Email == identity.Email {
			matches++
			if item.Membership.Role != domain.WorkspaceRoleMember || !item.Membership.Active {
				t.Fatalf("provisioned membership=%+v", item.Membership)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("provisioned users with email %q=%d, want 1", identity.Email, matches)
	}
}

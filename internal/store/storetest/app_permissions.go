package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// AppPermissionRepository is the part of store.Store the app access control
// check drives.
type AppPermissionRepository interface {
	CreateApp(context.Context, domain.App, domain.AppManifestRevision, domain.OAuthClient) error
	GetAppPermission(context.Context, domain.WorkspaceID, domain.AppID) (domain.AppPermission, error)
	SetAppPermission(context.Context, domain.AppPermission, events.Event) error
	ListMCPServerPermissions(context.Context, domain.WorkspaceID, domain.AppID) ([]domain.MCPServerPermission, error)
	SetMCPServerPermission(context.Context, domain.MCPServerPermission, events.Event) error
}

// CheckAppPermissions requires an app's access control list and its MCP server
// permissions to be stored whole, replaced whole, read back with empty lists
// rather than nil, kept apart per app, and refused for an app that does not
// exist or a list that breaks the access control invariant. The repository
// must hold workspace T1 with member U1.
func CheckAppPermissions(t *testing.T, repository AppPermissionRepository) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, app := range []domain.AppID{"AP1", "AP2"} {
		if err := repository.CreateApp(ctx, domain.App{
			ID: app, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Permissions " + string(app), ClientID: "client-" + string(app),
			SigningSecretHash: "hash", SigningSecretCiphertext: "cipher", VerificationTokenHash: "hash", VerificationTokenCiphertext: "cipher",
			ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
		}, domain.AppManifestRevision{AppID: app, Version: 1, Manifest: `{"display_information":{"name":"Permissions"}}`, CreatedBy: "U1", CreatedAt: now},
			domain.OAuthClient{ID: "client-" + string(app), SecretHash: "secret", AppID: app}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := repository.GetAppPermission(ctx, "T1", "AP1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unset list err=%v, want ErrNotFound", err)
	}
	named := domain.AppPermission{
		AppID: "AP1", WorkspaceID: "T1", PermissionType: domain.AppPermissionNamedEntities,
		UserIDs: []domain.UserID{"U1"}, UserGroupIDs: []domain.UserGroupID{"S1"},
		ChannelRestrictionMode: domain.ChannelRestrictionAllChannelsExcept, ChannelIDs: []domain.ConversationID{"C1", "C2"},
		UpdatedAt: now,
	}
	if err := repository.SetAppPermission(ctx, named, mustAppEvent(t, "Ev-acl-1", now)); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetAppPermission(ctx, "T1", "AP1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, named) {
		t.Fatalf("stored=%+v, want %+v", stored, named)
	}
	// A replacement is whole: the lists it leaves out are gone, and read back
	// as empty rather than nil.
	everyone := domain.AppPermission{AppID: "AP1", WorkspaceID: "T1", PermissionType: domain.AppPermissionEveryone, UpdatedAt: now.Add(time.Second)}
	if err := repository.SetAppPermission(ctx, everyone, mustAppEvent(t, "Ev-acl-2", now)); err != nil {
		t.Fatal(err)
	}
	stored, err = repository.GetAppPermission(ctx, "T1", "AP1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PermissionType != domain.AppPermissionEveryone || stored.ChannelRestrictionMode != domain.ChannelRestrictionUnset ||
		stored.UserIDs == nil || len(stored.UserIDs) != 0 || stored.UserGroupIDs == nil || stored.ChannelIDs == nil || len(stored.ChannelIDs) != 0 ||
		!stored.UpdatedAt.Equal(everyone.UpdatedAt) {
		t.Fatalf("replaced=%+v", stored)
	}
	if _, err := repository.GetAppPermission(ctx, "T1", "AP2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another app's list err=%v, want ErrNotFound", err)
	}
	unknown := everyone
	unknown.AppID = "A-none"
	if err := repository.SetAppPermission(ctx, unknown, mustAppEvent(t, "Ev-acl-3", now)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown app err=%v, want ErrNotFound", err)
	}
	// The invariant holds at the store too: a list that names entities is a
	// named_entities list.
	broken := everyone
	broken.UserIDs = []domain.UserID{"U1"}
	if err := repository.SetAppPermission(ctx, broken, mustAppEvent(t, "Ev-acl-4", now)); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("broken list err=%v, want ErrInvalidArgument", err)
	}

	if values, err := repository.ListMCPServerPermissions(ctx, "T1", "AP1"); err != nil || len(values) != 0 {
		t.Fatalf("no server permissions = %v, %v", values, err)
	}
	server := domain.MCPServerPermission{
		WorkspaceID: "T1", AppID: "AP1", ServerID: "AmcpB", PermissionType: domain.MCPServerPermissionNamedEntitiesExclude,
		UserIDs: []domain.UserID{"U1"}, UserGroupIDs: []domain.UserGroupID{}, UpdatedAt: now,
	}
	other := domain.MCPServerPermission{
		WorkspaceID: "T1", AppID: "AP1", ServerID: "AmcpA", PermissionType: domain.MCPServerPermissionNoOne,
		UserIDs: []domain.UserID{}, UserGroupIDs: []domain.UserGroupID{}, UpdatedAt: now,
	}
	for index, value := range []domain.MCPServerPermission{server, other} {
		if err := repository.SetMCPServerPermission(ctx, value, mustAppEvent(t, domain.EventID("Ev-mcp-"+string(rune('a'+index))), now)); err != nil {
			t.Fatal(err)
		}
	}
	replaced := server
	replaced.PermissionType, replaced.UserIDs = domain.MCPServerPermissionEveryone, []domain.UserID{}
	if err := repository.SetMCPServerPermission(ctx, replaced, mustAppEvent(t, "Ev-mcp-c", now)); err != nil {
		t.Fatal(err)
	}
	values, err := repository.ListMCPServerPermissions(ctx, "T1", "AP1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, []domain.MCPServerPermission{other, replaced}) {
		t.Fatalf("server permissions=%+v, want ordered by server and replaced whole", values)
	}
	if values, err := repository.ListMCPServerPermissions(ctx, "T1", "AP2"); err != nil || len(values) != 0 {
		t.Fatalf("another app's server permissions = %v, %v", values, err)
	}
	orphan := other
	orphan.AppID = "A-none"
	if err := repository.SetMCPServerPermission(ctx, orphan, mustAppEvent(t, "Ev-mcp-d", now)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown app server permission err=%v, want ErrNotFound", err)
	}
}

func mustAppEvent(t *testing.T, id domain.EventID, at time.Time) events.Event {
	t.Helper()
	event, err := events.New(id, "T1", "U1", events.NewPayload("app.permissions_set", events.String("app_id", "AP1")), at)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

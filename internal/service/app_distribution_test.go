package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestSetAppDistributionRequiresOwnerAndRedirect covers activating public
// distribution: it needs a redirect URL (an install has nowhere to return to
// without one), toggles both ways, and only the app's owner may change it.
func TestSetAppDistributionRequiresOwnerAndRedirect(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "other"})
	m := Messages{Store: s, AppCredentialKey: []byte("0123456789abcdef0123456789abcdef")}
	configuration, err := m.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}

	// An app with no redirect URL cannot be distributed.
	noRedirect := `{"display_information":{"name":"NoRedirect"},"oauth_config":{"scopes":{"bot":["chat:write"]}},"settings":{"socket_mode_enabled":true}}`
	bare, _, err := m.CreateAppFromManifest(ctx, configuration.Token, noRedirect, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetAppDistribution(ctx, configuration.Token, bare.ID, true); !errors.Is(err, ErrAppNotDistributable) {
		t.Fatalf("distribute without redirect = %v, want ErrAppNotDistributable", err)
	}

	// An app with a redirect URL activates and deactivates.
	withRedirect := `{"display_information":{"name":"App"},"oauth_config":{"redirect_urls":["https://example.test/oauth"],"scopes":{"bot":["chat:write"]}},"settings":{"socket_mode_enabled":true}}`
	app, _, err := m.CreateAppFromManifest(ctx, configuration.Token, withRedirect, "")
	if err != nil {
		t.Fatal(err)
	}
	if app.Distribution != "private" {
		t.Fatalf("new app distribution = %q, want private", app.Distribution)
	}
	distributed, err := m.SetAppDistribution(ctx, configuration.Token, app.ID, true)
	if err != nil || distributed.Distribution != "public" {
		t.Fatalf("activate = %+v err=%v, want public", distributed, err)
	}
	// The change is durable.
	if reloaded, _, _ := s.GetApp(ctx, app.ID); reloaded.Distribution != "public" {
		t.Fatalf("stored distribution = %q, want public", reloaded.Distribution)
	}
	reprivatized, err := m.SetAppDistribution(ctx, configuration.Token, app.ID, false)
	if err != nil || reprivatized.Distribution != "private" {
		t.Fatalf("deactivate = %+v err=%v, want private", reprivatized, err)
	}

	// Another member's configuration token cannot change this app: it is not
	// theirs, and it answers as a missing one.
	otherConfiguration, err := m.IssueAppConfigurationToken(ctx, "T1", "U2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetAppDistribution(ctx, otherConfiguration.Token, app.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-owner distribute = %v, want ErrNotFound", err)
	}
}

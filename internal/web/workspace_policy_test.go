package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// restrictWorkspace sets the policy as a separate administrator, so the
// signed-in member U1 keeps the plain member role the fixture gives them.
func restrictWorkspace(t *testing.T, s *memory.Store, policy domain.WorkspacePolicy) {
	t.Helper()
	if err := s.SeedUser(domain.User{ID: "Uadmin", WorkspaceID: "T1", Name: "administrator"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspaceRole("T1", "Uadmin", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := (service.Messages{Store: s}).SetWorkspacePolicy(context.Background(), "T1", "Uadmin", policy); err != nil {
		t.Fatal(err)
	}
}

// The permissions control writes through, reads back, and refuses an
// audience the product does not apply rather than storing it.
func TestPermissionsControlWritesThroughAndReadsBack(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	page := func() string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/app/admin/settings", nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	initial := page()
	for _, fragment := range []string{
		`action="/app/admin/settings/permissions"`,
		`<legend>Channel notifications</legend>`,
		`name="broadcast_warning" value="on" aria-describedby="broadcast-warning-detail" checked`,
		`<legend>Who can create private channels</legend>`,
		`value="everyone" aria-describedby="private-creators-everyone" checked`,
		"Converting Slack Connect group DMs.",
	} {
		if !strings.Contains(initial, fragment) {
			t.Fatalf("the settings page lacks %q: %s", fragment, initial)
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/app/admin/settings/permissions", "private_channel_creators=owners"))
	if response.Code != http.StatusOK {
		t.Fatalf("save=%d body=%s", response.Code, response.Body.String())
	}
	policy, err := store.GetWorkspacePolicy(ctx, "T1")
	if err != nil || !policy.BroadcastWarningOff || policy.PrivateChannelCreators != domain.PolicyAudienceOwners {
		t.Fatalf("an unchecked warning and owners-only saved %+v err=%v", policy, err)
	}
	saved := page()
	if strings.Contains(saved, `value="on" aria-describedby="broadcast-warning-detail" checked`) || !strings.Contains(saved, `value="owners" aria-describedby="private-creators-owners" checked`) {
		t.Fatalf("the page does not reflect what was saved: %s", saved)
	}

	refused := httptest.NewRecorder()
	handler.ServeHTTP(refused, adminMutationRequest(http.MethodPost, "/app/admin/settings/permissions", "broadcast_warning=on&private_channel_creators=guests"))
	if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "invalid_policy") {
		t.Fatalf("an unknown audience=%d body=%s, want a 400 invalid_policy", refused.Code, refused.Body.String())
	}
	if unchanged, err := store.GetWorkspacePolicy(ctx, "T1"); err != nil || unchanged != policy {
		t.Fatalf("a refused save changed the policy to %+v err=%v", unchanged, err)
	}
}

func TestPermissionsControlRefusesAMember(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleMember)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/app/admin/settings/permissions", "private_channel_creators=owners"))
	if response.Code != http.StatusForbidden {
		t.Fatalf("a member saving permissions=%d body=%s, want 403", response.Code, response.Body.String())
	}
	if policy, err := store.GetWorkspacePolicy(context.Background(), "T1"); err != nil || policy != domain.DefaultWorkspacePolicy() {
		t.Fatalf("a refused member changed the policy to %+v err=%v", policy, err)
	}
}

// The composer reads the workspace's broadcast-warning choice from its form,
// so the server has to put it there exactly when the workspace turned it off.
func TestComposerCarriesTheBroadcastWarningPolicy(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	// The script names the attribute too, so only the form's own tag counts.
	composerTag := func() string {
		t.Helper()
		body := get(t, mux, "/app?channel=Cdev").Body.String()
		start := strings.Index(body, `<form class="composer`)
		if start < 0 {
			t.Fatalf("no composer form: %s", body)
		}
		return body[start : start+strings.Index(body[start:], ">")]
	}
	before := composerTag()
	requireContains(t, "composer", before, `data-member-count=`)
	requireMissing(t, "composer with the warning on", before, "data-broadcast-warning-off")

	restrictWorkspace(t, s, domain.WorkspacePolicy{BroadcastWarningOff: true, PrivateChannelCreators: domain.PolicyAudienceEveryone})
	requireContains(t, "composer with the warning off", composerTag(), "data-broadcast-warning-off")
}

func TestCreateChannelFollowsThePrivateChannelPolicy(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	open := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "dialog for a permitted member", open, "new-channel-private-restricted")

	restrictWorkspace(t, s, domain.WorkspacePolicy{PrivateChannelCreators: domain.PolicyAudienceAdmins})
	restricted := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "dialog for a restricted member", restricted,
		`name="is_private" value="true" disabled aria-describedby="new-channel-private-restricted"`,
		"Your workspace limits who can create private channels.")

	refused := postForm(t, mux, "/app/conversation/create", "name=secret-plans&is_private=true", false)
	if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "Your workspace limits who can create private channels") {
		t.Fatalf("a restricted private create=%d body=%s, want a 403 that says why", refused.Code, refused.Body)
	}
	if public := postForm(t, mux, "/app/conversation/create", "name=open-plans&is_private=false", false); public.Code != http.StatusSeeOther {
		t.Fatalf("a public create under the policy=%d body=%s", public.Code, public.Body)
	}
}

func TestGroupDirectConversionFollowsThePrivateChannelPolicy(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	for _, id := range []domain.UserID{"U2", "U3"} {
		if err := s.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	opening, err := (service.Messages{Store: s}).OpenConversation(context.Background(), "T1", "U1", []domain.UserID{"U2", "U3"})
	if err != nil {
		t.Fatal(err)
	}
	group := string(opening.Conversation.ID)
	requireContains(t, "group DM settings", get(t, mux, "/app?channel="+group+"&details=1").Body.String(), "Change to a private channel")

	restrictWorkspace(t, s, domain.WorkspacePolicy{PrivateChannelCreators: domain.PolicyAudienceAdmins})
	requireMissing(t, "group DM settings for a restricted member", get(t, mux, "/app?channel="+group+"&details=1").Body.String(), "Change to a private channel")
	refused := postForm(t, mux, "/app/conversation/convert-to-private?channel="+group, "name=project-room", false)
	if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "Your workspace limits who can create private channels") {
		t.Fatalf("a restricted conversion=%d body=%s, want a 403 that says why", refused.Code, refused.Body)
	}
	if unchanged, err := s.GetConversation(context.Background(), opening.Conversation.ID); err != nil || unchanged.Kind != domain.ConversationTypeMPIM {
		t.Fatalf("a refused conversion left %+v err=%v", unchanged, err)
	}
}

// A guest's refusal to create a channel was reported as the store being
// unavailable, which sent them to retry something their account never allows.
func TestCreateChannelTellsAGuestWhyItWasRefused(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	guest := domain.User{ID: "UG", WorkspaceID: "T1", Email: "guest@example.com", Name: "guest"}
	if err := s.CreateUser(context.Background(), guest, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "UG", Role: domain.WorkspaceRoleMember, Active: true, Restricted: true},
		events.Event{ID: "E-guest", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(context.Background(), "guest-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "UG", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/app/conversation/create", strings.NewReader(url.Values{"_csrf": {auth.CSRFToken("guest-session")}, "name": {"guest-room"}, "is_private": {"false"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "guest-session"})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "Guests cannot create channels") {
		t.Fatalf("a guest creating a channel=%d body=%s, want a 403 that says why", response.Code, response.Body)
	}
}

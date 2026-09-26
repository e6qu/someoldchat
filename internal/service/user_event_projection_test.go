package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/slackobject"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// The user object a user_change / user_profile_changed event carries is the
// one users.info returns for the same user at the same instant, on the
// deployment's public URL. It used to be a hand-built subset that called every
// bot a person and carried image paths with no host.
func TestUserChangeEventCarriesTheUsersInfoObjectOnThePublicURL(t *testing.T) {
	ctx := context.Background()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1"})
	if err := state.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := state.SeedToken(ctx, "xoxb-A1", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: []string{"users:read"}}); err != nil {
		t.Fatal(err)
	}
	const origin = "https://chat.example.test"
	at := time.Unix(1_700_000_000, 0).UTC()
	subject := domain.User{
		ID: "UOTHER", WorkspaceID: "T1", Name: "helper", RealName: "Helper Bot", Email: "secret@example.com",
		BotID: "B9", AppID: "A9", Updated: at,
		Profile: domain.UserProfile{DisplayName: "helper", StatusText: "on call", Image24: "/users/T1/UOTHER/photo/abc123", Image512: "https://images.example.com/512.png"},
	}
	payload, err := events.UserChangePayload("user.profile_changed", subject, false, true, at)
	if err != nil {
		t.Fatal(err)
	}
	event, err := newEvent("T1", "UOTHER", payload, at)
	if err != nil {
		t.Fatal(err)
	}
	prepared, visible, err := PrepareAppEvent(ctx, state, appEventTestKey, origin, "A1", events.Record{Sequence: 1, Event: event})
	if err != nil || !visible {
		t.Fatalf("visible=%v err=%v", visible, err)
	}
	bodies, err := events.SlackEventBodies(prepared, "A1")
	if err != nil || len(bodies) == 0 {
		t.Fatalf("bodies=%q err=%v", bodies, err)
	}
	want, err := json.Marshal(slackobject.User(origin, subject, false))
	if err != nil {
		t.Fatal(err)
	}
	var wantObject map[string]any
	if err := json.Unmarshal(want, &wantObject); err != nil {
		t.Fatal(err)
	}
	for _, body := range bodies {
		var envelope struct {
			Event struct {
				Type string         `json:"type"`
				User map[string]any `json:"user"`
			} `json:"event"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(envelope.Event.User, wantObject) {
			t.Fatalf("%s user object\n got %v\nwant %v", envelope.Event.Type, envelope.Event.User, wantObject)
		}
	}
	profile := wantObject["profile"].(map[string]any)
	if wantObject["is_bot"] != true || profile["image_24"] != origin+"/users/T1/UOTHER/photo/abc123" ||
		profile["image_512"] != "https://images.example.com/512.png" || profile["image_48"] != origin+"/avatars/T1/UOTHER/48.png" {
		t.Fatalf("users.info object is not the expected shape: %v", wantObject)
	}
	if _, leaked := profile["email"]; leaked {
		t.Fatal("the journal snapshot must not carry an e-mail address")
	}

	// A deployment with no public URL keeps the stored origin-relative paths.
	prepared, _, err = PrepareAppEvent(ctx, state, appEventTestKey, "", "A1", events.Record{Sequence: 1, Event: event})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := events.Deliverable(prepared.Event)
	if err != nil {
		t.Fatal(err)
	}
	var relative map[string]any
	if err := json.Unmarshal(delivered.Object["user"], &relative); err != nil {
		t.Fatal(err)
	}
	if image := relative["profile"].(map[string]any)["image_48"]; image != "/avatars/T1/UOTHER/48.png" {
		t.Fatalf("relative image_48 = %v", image)
	}
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// linkSharedFixture is a workspace with three installed apps: A1 claims
// example.com, A2 claims other.test, and A3 claims example.com without
// links:read. CPUB is a public channel A1's bot has joined, COPEN a public
// channel it has not, CPRIV a private channel it has not.
func linkSharedFixture(t *testing.T) (*memory.Store, Messages) {
	t.Helper()
	ctx := context.Background()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	for _, user := range []domain.UserID{"U1", "UB1", "UB2", "UB3"} {
		state.SeedUser(domain.User{ID: user, WorkspaceID: "T1", Name: string(user)})
	}
	state.SeedConversation(domain.Conversation{ID: "CPUB", WorkspaceID: "T1", Name: "general"})
	state.SeedConversation(domain.Conversation{ID: "COPEN", WorkspaceID: "T1", Name: "open"})
	state.SeedConversation(domain.Conversation{ID: "CPRIV", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
	for _, conversation := range []domain.ConversationID{"CPUB", "COPEN", "CPRIV"} {
		state.SeedConversationMember(conversation, "U1")
	}
	state.SeedConversationMember("CPUB", "UB1")
	now := time.Now().UTC()
	for _, app := range []struct {
		id     domain.AppID
		bot    domain.UserID
		domain string
		scopes []string
	}{
		{"A1", "UB1", "example.com", []string{"links:read", "links:write"}},
		{"A2", "UB2", "other.test", []string{"links:read", "links:write"}},
		{"A3", "UB3", "example.com", []string{"links:write"}},
	} {
		manifest := `{"display_information":{"name":"` + string(app.id) + `"},"features":{"unfurl_domains":["` + app.domain + `"]},` +
			`"oauth_config":{"scopes":{"bot":["links:read","links:write"]}},"settings":{"socket_mode_enabled":true,"event_subscriptions":{"bot_events":["link_shared"]}}}`
		client := "client-" + string(app.id)
		if err := state.CreateApp(ctx,
			domain.App{ID: app.id, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: string(app.id), ClientID: client, SigningSecretHash: "hash", SigningSecretCiphertext: "sealed", VerificationTokenCiphertext: "sealed", ManifestVersion: 1, CreatedAt: now, UpdatedAt: now},
			domain.AppManifestRevision{AppID: app.id, Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
			domain.OAuthClient{ID: client, SecretHash: "secret", AppID: app.id},
		); err != nil {
			t.Fatal(err)
		}
		botID := domain.BotID("B" + string(app.id))
		if err := state.CreateBot(ctx, domain.Bot{ID: botID, WorkspaceID: "T1", AppID: app.id, UserID: app.bot, Name: string(app.id), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := state.SeedToken(ctx, "xoxb-"+string(app.id), domain.TokenRecord{WorkspaceID: "T1", UserID: app.bot, AppID: app.id, BotID: botID, TokenType: "bot", Scopes: app.scopes}); err != nil {
			t.Fatal(err)
		}
		if err := state.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.id, WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return state, Messages{Store: state, AppCredentialKey: appEventTestKey}
}

// linkSharedRecords returns the link.shared records journalled since mark.
func linkSharedRecords(state *memory.Store, mark int) ([]events.Event, int) {
	outbox := state.Outbox()
	var shared []events.Event
	for _, event := range outbox[mark:] {
		if event.Topic == linkSharedTopic {
			shared = append(shared, event)
		}
	}
	return shared, len(outbox)
}

type linkSharedCallback struct {
	Type            string              `json:"type"`
	Channel         string              `json:"channel"`
	IsBotUserMember bool                `json:"is_bot_user_member"`
	User            string              `json:"user"`
	MessageTS       string              `json:"message_ts"`
	ThreadTS        string              `json:"thread_ts"`
	UnfurlID        string              `json:"unfurl_id"`
	Source          string              `json:"source"`
	EventTS         string              `json:"event_ts"`
	Links           []domain.SharedLink `json:"links"`
}

// linkSharedFor projects a link.shared record for one app and decodes the
// callback it would receive, reporting false when the app receives none.
func linkSharedFor(t *testing.T, state *memory.Store, appID domain.AppID, event events.Event) (linkSharedCallback, bool) {
	t.Helper()
	prepared, visible, err := PrepareAppEvent(context.Background(), state, appEventTestKey, "", appID, events.Record{Sequence: 1, Event: event})
	if err != nil {
		t.Fatalf("%s: %v", appID, err)
	}
	if !visible {
		return linkSharedCallback{}, false
	}
	bodies, err := events.SlackEventBodies(prepared, string(appID))
	if err != nil {
		t.Fatalf("%s: %v", appID, err)
	}
	if len(bodies) != 1 {
		return linkSharedCallback{}, false
	}
	var envelope struct {
		Event linkSharedCallback `json:"event"`
	}
	if err := json.Unmarshal(bodies[0], &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Event, true
}

func TestPostedLinksReachEachAppWhoseUnfurlDomainsClaimThem(t *testing.T) {
	ctx := context.Background()
	state, messages := linkSharedFixture(t)
	mark := len(state.Outbox())
	root, err := messages.Post(ctx, "T1", "U1", "CPUB", "a thread with no links", "", "")
	if err != nil {
		t.Fatal(err)
	}
	rootTS := domain.NewMessageTimestamp(root.CreatedAt)
	if shared, next := linkSharedRecords(state, mark); len(shared) != 0 {
		t.Fatalf("a message without links journalled %d link.shared records", len(shared))
	} else {
		mark = next
	}
	reply, err := messages.Post(ctx, "T1", "U1", "CPUB",
		"see <https://www.example.com/a|a>, `https://example.com/code` and https://other.test/x", rootTS, "")
	if err != nil {
		t.Fatal(err)
	}
	replyTS := domain.NewMessageTimestamp(reply.CreatedAt)
	shared, mark := linkSharedRecords(state, mark)
	if len(shared) != 1 {
		t.Fatalf("link.shared records=%d, want one per message", len(shared))
	}
	callback, ok := linkSharedFor(t, state, "A1", shared[0])
	want := linkSharedCallback{
		Type: "link_shared", Channel: "CPUB", IsBotUserMember: true, User: "U1",
		MessageTS: string(replyTS), ThreadTS: string(rootTS),
		UnfurlID: domain.NewUnfurlID("CPUB", replyTS), Source: "conversations_history", EventTS: string(replyTS),
		Links: []domain.SharedLink{{Domain: "example.com", URL: "https://www.example.com/a"}},
	}
	if !ok || !reflect.DeepEqual(callback, want) {
		t.Fatalf("A1 callback=%+v ok=%v\nwant %+v", callback, ok, want)
	}
	if callback, ok := linkSharedFor(t, state, "A2", shared[0]); !ok || !reflect.DeepEqual(callback.Links, []domain.SharedLink{{Domain: "other.test", URL: "https://other.test/x"}}) || callback.IsBotUserMember {
		t.Fatalf("A2 is handed only its own domain's link: callback=%+v ok=%v", callback, ok)
	}
	if _, ok := linkSharedFor(t, state, "A3", shared[0]); ok {
		t.Fatal("an app without links:read received link_shared")
	}

	// A public channel the bot has not joined still reaches the app, which
	// is told so; a private one it cannot see does not.
	if _, err := messages.Post(ctx, "T1", "U1", "COPEN", "https://example.com/open", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "CPRIV", "https://example.com/private", "", ""); err != nil {
		t.Fatal(err)
	}
	shared, mark = linkSharedRecords(state, mark)
	if len(shared) != 2 {
		t.Fatalf("link.shared records=%d, want 2", len(shared))
	}
	if callback, ok := linkSharedFor(t, state, "A1", shared[0]); !ok || callback.IsBotUserMember || callback.ThreadTS != "" || callback.Channel != "COPEN" {
		t.Fatalf("public channel callback=%+v ok=%v", callback, ok)
	}
	if callback, ok := linkSharedFor(t, state, "A1", shared[1]); ok {
		t.Fatalf("a private channel the app cannot see reached it: %+v", callback)
	}

	// An edit shares only the links it adds.
	if _, err := messages.UpdateWithBlocksAndAttachments(ctx, "T1", "U1", "CPUB", replyTS,
		"see <https://www.example.com/a|a> and https://example.com/new", "", ""); err != nil {
		t.Fatal(err)
	}
	shared, mark = linkSharedRecords(state, mark)
	if len(shared) != 1 {
		t.Fatalf("edit journalled %d link.shared records", len(shared))
	}
	if callback, ok := linkSharedFor(t, state, "A1", shared[0]); !ok || callback.MessageTS != string(replyTS) ||
		!reflect.DeepEqual(callback.Links, []domain.SharedLink{{Domain: "example.com", URL: "https://example.com/new"}}) {
		t.Fatalf("edit callback=%+v ok=%v", callback, ok)
	}
	if _, err := messages.UpdateWithBlocksAndAttachments(ctx, "T1", "U1", "CPUB", replyTS, "https://example.com/new", "", ""); err != nil {
		t.Fatal(err)
	}
	if shared, _ := linkSharedRecords(state, mark); len(shared) != 0 {
		t.Fatalf("an edit that adds no link journalled %d link.shared records", len(shared))
	}
}

func TestAnAppUnfurlsItsOwnDomainsInAPublicChannelItHasNotJoined(t *testing.T) {
	ctx := context.Background()
	_, messages := linkSharedFixture(t)
	posted, err := messages.Post(ctx, "T1", "U1", "COPEN", "https://example.com/open and https://other.test/y", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ts := domain.NewMessageTimestamp(posted.CreatedAt)
	unfurled, err := messages.Unfurl(ctx, "T1", "UB1", "A1", "COPEN", ts, map[string]string{"https://example.com/open": `{"title":"Open"}`})
	if err != nil || unfurled.Unfurls["https://example.com/open"] != `{"title":"Open"}` {
		t.Fatalf("unfurled=%+v err=%v", unfurled.Unfurls, err)
	}
	// Another app's link in the same message is not this app's to unfurl.
	if _, err := messages.Unfurl(ctx, "T1", "UB1", "A1", "COPEN", ts, map[string]string{"https://other.test/y": `{"title":"Other"}`}); !errors.Is(err, domain.ErrCannotUnfurlURL) {
		t.Fatalf("foreign-domain unfurl err=%v", err)
	}
	// A caller that is no app keeps the membership rule.
	if _, err := messages.Unfurl(ctx, "T1", "UB1", "", "COPEN", ts, map[string]string{"https://example.com/open": `{"title":"Open"}`}); !errors.Is(err, domain.ErrNotInConversation) {
		t.Fatalf("non-app non-member err=%v", err)
	}
	private, err := messages.Post(ctx, "T1", "U1", "CPRIV", "https://example.com/private", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Unfurl(ctx, "T1", "UB1", "A1", "CPRIV", domain.NewMessageTimestamp(private.CreatedAt), map[string]string{"https://example.com/private": `{"title":"P"}`}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("private channel unfurl err=%v", err)
	}
}

// Socket Mode claims go through the same projection and the manifest's
// subscription: the app's socket receives link_shared for its own links.
func TestSocketModeDeliversLinkSharedToTheClaimingApp(t *testing.T) {
	ctx := context.Background()
	_, messages := linkSharedFixture(t)
	posted, err := messages.Post(ctx, "T1", "U1", "CPUB", "read <https://docs.example.com/p/1>", "", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		claim, claimed, err := messages.ClaimAppEvent(ctx, "A1", "socket", "conn-1", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if claimed && claim.Record.Event.Topic == linkSharedTopic {
			bodies, err := events.SlackEventBodies(claim.Record, "A1")
			if err != nil || len(bodies) != 1 {
				t.Fatalf("bodies=%q err=%v", bodies, err)
			}
			var envelope struct {
				Type  string             `json:"type"`
				Event linkSharedCallback `json:"event"`
			}
			if err := json.Unmarshal(bodies[0], &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Type != "event_callback" || envelope.Event.Type != "link_shared" ||
				envelope.Event.MessageTS != string(domain.NewMessageTimestamp(posted.CreatedAt)) ||
				!reflect.DeepEqual(envelope.Event.Links, []domain.SharedLink{{Domain: "example.com", URL: "https://docs.example.com/p/1"}}) {
				t.Fatalf("socket body=%s", bodies[0])
			}
			return
		}
		if claimed {
			if err := messages.AckAppEvent(ctx, "A1", "socket", "conn-1", claim.Record.Sequence); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if time.Now().After(deadline) {
			t.Fatal("the socket never received link_shared")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An explicit unfurl_links decides whether a message hands its links to
// apps; a message that omits it does, whoever posted it, as before the flag
// was applied. The scheduled path carries the flag to delivery, and an edit
// keeps the choice the message was posted with.
func TestUnfurlLinksDecidesWhetherAppsAreAskedToUnfurl(t *testing.T) {
	ctx := context.Background()
	state, messages := linkSharedFixture(t)
	yes, no := true, false
	for _, test := range []struct {
		name    string
		request domain.MessagePostRequest
		author  domain.UserID
		shared  bool
	}{
		{"bot default", domain.MessagePostRequest{AppID: "A1", BotID: "BA1"}, "UB1", true},
		{"bot unfurl_links", domain.MessagePostRequest{AppID: "A1", BotID: "BA1", UnfurlLinks: &yes}, "UB1", true},
		{"bot unfurl_links=false", domain.MessagePostRequest{AppID: "A1", BotID: "BA1", UnfurlLinks: &no}, "UB1", false},
		{"bot unfurl_media=false", domain.MessagePostRequest{AppID: "A1", BotID: "BA1", UnfurlMedia: &no}, "UB1", true},
		{"person default", domain.MessagePostRequest{}, "U1", true},
		{"person through an app's user token", domain.MessagePostRequest{AppID: "A1"}, "U1", true},
		{"person unfurl_links=false", domain.MessagePostRequest{UnfurlLinks: &no}, "U1", false},
	} {
		request := test.request
		request.Conversation, request.Text = "CPUB", "see https://example.com/"+strings.ReplaceAll(test.name, " ", "-")
		mark := len(state.Outbox())
		posted, err := messages.PostMessageAs(ctx, "T1", test.author, request)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		shared, next := linkSharedRecords(state, mark)
		if (len(shared) == 1) != test.shared || len(shared) > 1 {
			t.Fatalf("%s: link.shared records=%d, want shared=%v", test.name, len(shared), test.shared)
		}
		text := posted.Text + " https://example.com/added"
		if _, err := messages.UpdateMessage(ctx, "T1", test.author, "CPUB", domain.NewMessageTimestamp(posted.CreatedAt), domain.MessagePatch{Text: &text}); err != nil {
			t.Fatalf("%s edit: %v", test.name, err)
		}
		if edited, _ := linkSharedRecords(state, next); (len(edited) == 1) != test.shared || len(edited) > 1 {
			t.Fatalf("%s: edit link.shared records=%d, want shared=%v", test.name, len(edited), test.shared)
		}
	}

	// Deferred delivery keeps the flag the message was scheduled with.
	for _, unfurl := range []*bool{nil, &no} {
		streamState := ""
		if unfurl != nil {
			streamState = `{"unfurl_links":false}`
		}
		scheduled, err := messages.ScheduleMessageAs(ctx, "T1", "UB1", domain.ScheduledMessageRequest{
			Channel: "CPUB", Text: "later https://example.com/scheduled", StreamState: streamState,
			PostAt: time.Now().Add(time.Hour), AppID: "A1", BotID: "BA1", CredentialHash: "xoxb-A1",
		})
		if err != nil {
			t.Fatal(err)
		}
		mark := len(state.Outbox())
		if _, err := messages.PostScheduledMessage(ctx, "T1", scheduled.ID); err != nil {
			t.Fatal(err)
		}
		if shared, _ := linkSharedRecords(state, mark); (len(shared) == 1) != (unfurl == nil) {
			t.Fatalf("scheduled bot message unfurl_links=false given=%v: link.shared records=%d", unfurl != nil, len(shared))
		}
	}
}

// An incoming webhook's payload chooses unfurl_links as a bot's chat.postMessage
// does: the official SDKs' webhook clients send it (Python WebhookClient.send,
// Java com.slack.api.webhook.Payload). false keeps the hook's links from apps;
// left out, they are handed over as any message's are.
func TestIncomingWebhookUnfurlLinksDecidesWhetherAppsAreAskedToUnfurl(t *testing.T) {
	ctx := context.Background()
	state, messages := linkSharedFixture(t)
	if err := state.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	_, secret, err := messages.AdminCreateIncomingWebhook(ctx, "T1", "U1", "A1", "CPUB", "UB1")
	if err != nil {
		t.Fatal(err)
	}
	no := false
	for _, test := range []struct {
		name   string
		unfurl *bool
		shared bool
	}{
		{"omitted", nil, true},
		{"unfurl_links=false", &no, false},
	} {
		mark := len(state.Outbox())
		if _, err := messages.PostIncomingWebhookWithAttachments(ctx, "T1", "A1", secret, domain.IncomingWebhookPost{
			Text: "hook https://example.com/" + strings.ReplaceAll(test.name, "=", "-"), UnfurlLinks: test.unfurl,
		}); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if shared, _ := linkSharedRecords(state, mark); (len(shared) == 1) != test.shared || len(shared) > 1 {
			t.Fatalf("%s: link.shared records=%d, want shared=%v", test.name, len(shared), test.shared)
		}
	}
}

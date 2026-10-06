package slack

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// seedOnCallGroup gives the fixture an enabled user group @oncall and a
// disabled one @retired, so a test can tell a resolved handle from an
// unresolved one.
func seedOnCallGroup(t *testing.T, repository *memory.Store) {
	t.Helper()
	now := time.Now().UTC()
	for _, group := range []domain.UserGroup{
		{WorkspaceID: "T1", ID: "S1", Name: "On call", Handle: "oncall", Enabled: true, Creator: "U1", CreatedAt: now, UpdatedAt: now},
		{WorkspaceID: "T1", ID: "S2", Name: "Retired", Handle: "retired", Enabled: false, Creator: "U1", CreatedAt: now, UpdatedAt: now},
	} {
		event := events.Event{ID: domain.EventID("E" + string(group.ID)), WorkspaceID: "T1", Topic: "usergroup.created", Payload: string(group.ID), CreatedAt: now}
		if err := repository.CreateUserGroup(context.Background(), group, event); err != nil {
			t.Fatal(err)
		}
	}
}

// linkNamesText names a member by username and by display name, a channel,
// an enabled and a disabled user group, things that resolve to nothing, and
// names inside code and existing markup that must not be touched.
const linkNamesText = "hi @bob and @alice. see #general, ping @oncall and @retired; " +
	"@nobody #nowhere mail bob@example.com `@bob` <https://example.com/x|@bob> <@U2>"

const linkNamesLinked = "hi <@U2> and <@U1>. see <#C1>, ping <!subteam^S1> and @retired; " +
	"@nobody #nowhere mail bob@example.com `@bob` <https://example.com/x|@bob> <@U2>"

func TestLinkNamesLinksMembersChannelsAndUserGroups(t *testing.T) {
	handler, repository := testHandlerWithStore()
	seedOnCallGroup(t, repository)

	linked := slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {linkNamesText}, "link_names": {"true"}})
	requireOK(t, "chat.postMessage", linked)
	if linked["message"].(map[string]any)["text"] != linkNamesLinked {
		t.Fatalf("link_names=true text = %q, want %q", linked["message"].(map[string]any)["text"], linkNamesLinked)
	}
	history := messagesOf(t, slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}, "limit": {"1"}}))
	if history[0]["text"] != linkNamesLinked {
		t.Fatalf("stored text = %q", history[0]["text"])
	}
	// The linked user group is a mention the message carries.
	if mentions := domain.MentionedUserGroups(linkNamesLinked); len(mentions) != 1 || mentions[0] != "S1" {
		t.Fatalf("user group mentions = %v", mentions)
	}

	for _, form := range []url.Values{
		{"channel": {"C1"}, "text": {linkNamesText}},
		{"channel": {"C1"}, "text": {linkNamesText}, "link_names": {"false"}},
		{"channel": {"C1"}, "text": {linkNamesText}, "link_names": {"0"}},
		// mrkdwn=false text is shown unparsed, so linking it would only print
		// raw markup.
		{"channel": {"C1"}, "text": {linkNamesText}, "link_names": {"1"}, "mrkdwn": {"false"}},
	} {
		literal := slackCall(t, handler, "token", "chat.postMessage", form)
		requireOK(t, "chat.postMessage", literal)
		if literal["message"].(map[string]any)["text"] != linkNamesText {
			t.Fatalf("%v: text = %q, want it as written", form, literal["message"].(map[string]any)["text"])
		}
	}

	// A malformed flag is a handled refusal, never a server error.
	requireError(t, "chat.postMessage", slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"x"}, "link_names": {"sometimes"}}), "invalid_arguments")
}

func TestLinkNamesOnlyLinksChannelsTheAuthorCanSee(t *testing.T) {
	handler, repository := testHandlerWithStore()
	repository.SeedConversation(domain.Conversation{ID: "G9", WorkspaceID: "T1", Name: "secret-plans", Kind: domain.ConversationTypePrivate})
	repository.SeedConversation(domain.Conversation{ID: "G8", WorkspaceID: "T1", Name: "my-private", Kind: domain.ConversationTypePrivate})
	repository.SeedConversationMember("G8", "U1")
	posted := slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"#secret-plans and #my-private"}, "link_names": {"true"}})
	requireOK(t, "chat.postMessage", posted)
	if got := posted["message"].(map[string]any)["text"]; got != "#secret-plans and <#G8>" {
		t.Fatalf("text = %q: a private channel the author is not in must stay literal", got)
	}
}

func TestScheduledMessageLinksNamesWhenDelivered(t *testing.T) {
	handler, repository := testHandlerWithStore()
	seedOnCallGroup(t, repository)
	scheduled := slackCall(t, handler, "token", "chat.scheduleMessage", url.Values{
		"channel": {"C1"}, "text": {"@bob @oncall #general"}, "link_names": {"true"},
		"post_at": {strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)},
	})
	requireOK(t, "chat.scheduleMessage", scheduled)
	message, err := service.Messages{Store: repository}.PostScheduledMessage(context.Background(), "T1", domain.ScheduledMessageID(scheduled["scheduled_message_id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "<@U2> <!subteam^S1> <#C1>" {
		t.Fatalf("delivered text = %q", message.Text)
	}
	requireError(t, "chat.scheduleMessage", slackCall(t, handler, "token", "chat.scheduleMessage", url.Values{
		"channel": {"C1"}, "text": {"x"}, "link_names": {"maybe"},
		"post_at": {strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)},
	}), "invalid_arguments")
}

func TestChatUpdateLinkNamesDescribesTheEdit(t *testing.T) {
	handler, repository := testHandlerWithStore()
	seedOnCallGroup(t, repository)
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"draft"}, "link_names": {"true"}})

	linked := slackCall(t, handler, "token", "chat.update", url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"@bob #general @oncall"}, "link_names": {"1"}})
	requireOK(t, "chat.update", linked)
	if linked["text"] != "<@U2> <#C1> <!subteam^S1>" {
		t.Fatalf("chat.update link_names=1 text = %q", linked["text"])
	}
	// An edit without link_names is not linked, though the message was posted
	// with it: the pinned reference overwrites an omitted value with none.
	for _, value := range []string{"", "none", "false"} {
		form := url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"@bob #general"}}
		if value != "" {
			form.Set("link_names", value)
		}
		literal := slackCall(t, handler, "token", "chat.update", form)
		requireOK(t, "chat.update", literal)
		if literal["text"] != "@bob #general" {
			t.Fatalf("chat.update link_names=%q text = %q", value, literal["text"])
		}
	}
	requireError(t, "chat.update", slackCall(t, handler, "token", "chat.update", url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"x"}, "link_names": {"maybe"}}), "invalid_arg_name")
}

func TestChatPostEphemeralLinkNames(t *testing.T) {
	handler, repository := testHandlerWithStore()
	seedOnCallGroup(t, repository)
	requireOK(t, "chat.postEphemeral", slackCall(t, handler, "token", "chat.postEphemeral", url.Values{"channel": {"C1"}, "user": {"U2"}, "text": {"@alice #general @oncall @nobody"}, "link_names": {"true"}}))
	requireOK(t, "chat.postEphemeral", slackCall(t, handler, "token", "chat.postEphemeral", url.Values{"channel": {"C1"}, "user": {"U2"}, "text": {"@alice as written"}}))
	delivered, err := service.Messages{Store: repository}.ListEphemeralMessages(context.Background(), "T1", "U2", "C1", 10)
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]bool{}
	for _, message := range delivered {
		texts[message.Text] = true
	}
	if !texts["<@U1> <#C1> <!subteam^S1> @nobody"] || !texts["@alice as written"] {
		t.Fatalf("ephemeral texts = %v", texts)
	}
	requireError(t, "chat.postEphemeral", slackCall(t, handler, "token", "chat.postEphemeral", url.Values{"channel": {"C1"}, "user": {"U2"}, "text": {"x"}, "link_names": {"maybe"}}), "invalid_arg_name")
}

// linkSharedCount is how many link.shared records the outbox holds: one per
// message whose links apps are asked to unfurl.
func linkSharedCount(repository *memory.Store) int {
	count := 0
	for _, event := range repository.Outbox() {
		if event.Topic == "link.shared" {
			count++
		}
	}
	return count
}

// An explicit unfurl_links decides whether the links of a message are handed
// to apps to unfurl (the pinned reference: "Pass true to enable unfurling of
// primarily text-based content"). A message that omits it is handed over,
// bot or person, as before the flag was applied; unfurl_media alone changes
// nothing, since the server makes no media unfurls.
func TestUnfurlLinksGatesTheLinksAppsAreAskedToUnfurl(t *testing.T) {
	for _, test := range []struct {
		name   string
		user   bool
		form   url.Values
		shared bool
	}{
		{"bot default", false, url.Values{}, true},
		{"bot unfurl_links=true", false, url.Values{"unfurl_links": {"true"}}, true},
		{"bot unfurl_links=false", false, url.Values{"unfurl_links": {"false"}}, false},
		{"bot unfurl_media=false alone", false, url.Values{"unfurl_media": {"false"}}, true},
		{"bot unfurl_links=true unfurl_media=false", false, url.Values{"unfurl_links": {"true"}, "unfurl_media": {"false"}}, true},
		{"user default", true, url.Values{}, true},
		{"user unfurl_links=false", true, url.Values{"unfurl_links": {"false"}}, false},
		{"user unfurl_media=false alone", true, url.Values{"unfurl_media": {"false"}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var handler http.Handler
			var repository *memory.Store
			if test.user {
				handler, repository = testUserHandlerWithStore()
			} else {
				handler, repository = testHandlerWithStore()
			}
			form := url.Values{"channel": {"C1"}, "text": {"see https://example.com/a"}}
			for key, values := range test.form {
				form[key] = values
			}
			before := linkSharedCount(repository)
			ts := post(t, handler, form)
			if got := linkSharedCount(repository) - before; got != map[bool]int{true: 1, false: 0}[test.shared] {
				t.Fatalf("link.shared records = %d, want shared=%v", got, test.shared)
			}
			// An edit that adds a link follows the same choice: the message
			// keeps the unfurl_links it was posted with.
			before = linkSharedCount(repository)
			requireOK(t, "chat.update", slackCall(t, handler, "token", "chat.update", url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"see https://example.com/a and https://example.com/b"}}))
			if got := linkSharedCount(repository) - before; got != map[bool]int{true: 1, false: 0}[test.shared] {
				t.Fatalf("edit link.shared records = %d, want shared=%v", got, test.shared)
			}
		})
	}

	handler, _ := testHandlerWithStore()
	for _, name := range []string{"unfurl_links", "unfurl_media"} {
		requireError(t, "chat.postMessage", slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"x"}, name: {"often"}}), "invalid_arguments")
	}
}

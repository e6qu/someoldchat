package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// chat.unfurl's user_auth_* arguments invite the member who shared a link to
// connect their account, in an ephemeral message only they see, carrying
// Slack's "Not now" and "Never ask me again". The second keeps the app from
// asking that member again; a malformed URL is refused before anything lands.
func TestChatUnfurlInvitesTheSharerToAuthenticate(t *testing.T) {
	handler, store := testFixture(false, "links:write")
	ctx := context.Background()
	shared := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	message := domain.Message{ID: "Mshared", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U2", Text: "see <https://example.com/doc>", CreatedAt: shared}
	if err := store.CreateMessage(ctx, message, events.Event{ID: "Eshared", WorkspaceID: "T1", Topic: "message.created", Payload: "Mshared", CreatedAt: shared}, ""); err != nil {
		t.Fatal(err)
	}
	ts := string(domain.NewMessageTimestamp(shared))
	unfurl := func(t *testing.T, extra string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/chat.unfurl", strings.NewReader("channel=C1&ts="+ts+"&unfurls="+url.QueryEscape(`{}`)+extra))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return strings.TrimSpace(response.Body.String())
	}
	prompts := func(t *testing.T) []domain.EphemeralMessage {
		t.Helper()
		values, err := store.ListEphemeralMessages(ctx, "T1", "U2", "C1", 10)
		if err != nil {
			t.Fatal(err)
		}
		return values
	}

	if body := unfurl(t, "&user_auth_url="+url.QueryEscape("javascript:alert(1)")); body != `{"error":"invalid_arg_name","ok":false}` {
		t.Fatalf("a script URL = %s, want invalid_arg_name", body)
	}
	if body := unfurl(t, "&user_auth_message="+url.QueryEscape("Connect to preview docs")+"&user_auth_url="+url.QueryEscape("https://app.example/connect")); body != `{"ok":true}` {
		t.Fatalf("prompt = %s", body)
	}
	sent := prompts(t)
	if len(sent) != 1 || sent[0].RecipientID != "U2" || sent[0].AppID != "A1" ||
		!strings.Contains(sent[0].Blocks, "Connect to preview docs") || !strings.Contains(sent[0].Blocks, "https://app.example/connect") ||
		!strings.Contains(sent[0].Blocks, domain.UnfurlAuthNotNowAction) || !strings.Contains(sent[0].Blocks, domain.UnfurlAuthNeverAskAction) {
		t.Fatalf("prompts = %+v", sent)
	}

	// The sharer's "Never ask me again" is answered by the product, without
	// the app, and dismisses the prompt.
	messages := service.Messages{Store: store}
	if err := messages.DispatchBlockAction(ctx, "T1", "U2", domain.AppBlockAction{MessageID: sent[0].ID, BlockID: domain.UnfurlAuthBlockID, ActionID: domain.UnfurlAuthNeverAskAction, Type: "button"}, ""); err != nil {
		t.Fatal(err)
	}
	if left := prompts(t); len(left) != 0 {
		t.Fatalf("the prompt survived its dismissal: %+v", left)
	}
	if body := unfurl(t, "&user_auth_required=true"); body != `{"ok":true}` {
		t.Fatalf("second prompt = %s", body)
	}
	if again := prompts(t); len(again) != 0 {
		t.Fatalf("a member who chose never to be asked was asked again: %+v", again)
	}
}

package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// A member a channel's who_can_post excludes is told so in place of the
// composer, before typing anything the send would refuse, and keeps the thread
// composer only while can_thread admits them.
func TestThePageWithholdsTheComposersAPostingRestrictionRefuses(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	at := time.Unix(1700000300, 0).UTC()
	root := domain.Message{ID: "Mroot", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: "a thread", CreatedAt: at}
	if err := s.CreateMessage(context.Background(), root, events.Event{ID: "Eroot", WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created"}`, CreatedAt: at}, ""); err != nil {
		t.Fatal(err)
	}
	thread := string(domain.NewMessageTimestamp(at))
	page := func() string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/app?channel=Cdev&thread="+thread, nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /app returned %d", recorder.Code)
		}
		return recorder.Body.String()
	}
	changes := 0
	restrict := func(prefs domain.ConversationPrefs) {
		t.Helper()
		changes++
		if _, err := s.SetConversationPrefs(context.Background(), "Cdev", prefs, events.Event{ID: domain.EventID(fmt.Sprintf("Eprefs%d", changes)), WorkspaceID: "T1", Topic: "conversation.prefs", Payload: `{}`, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	onlyU2 := domain.ConversationPreferenceList{Users: []domain.UserID{"U2"}}

	body := page()
	if !strings.Contains(body, `data-composer="channel"`) || !strings.Contains(body, `data-composer="thread"`) || strings.Contains(body, "Only some members can") {
		t.Fatal("an unrestricted channel lost a composer")
	}

	restrict(domain.ConversationPrefs{WhoCanPost: onlyU2})
	body = page()
	if strings.Contains(body, `data-composer="channel"`) || !strings.Contains(body, "Only some members can post in") || !strings.Contains(body, "You can still reply in its threads.") {
		t.Fatal("who_can_post did not replace the channel composer")
	}
	if !strings.Contains(body, `data-composer="thread"`) {
		t.Fatal("who_can_post alone took the thread composer away")
	}

	restrict(domain.ConversationPrefs{WhoCanPost: onlyU2, CanThread: onlyU2})
	body = page()
	if strings.Contains(body, `data-composer="thread"`) || !strings.Contains(body, "Only some members can reply to threads in") || !strings.Contains(body, "You can read its messages and react to them.") {
		t.Fatal("can_thread did not replace the thread composer")
	}
}

package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// Slack's "Hide a person": the hidden person's messages are still delivered
// but shown behind a click-through without their name, Privacy & visibility
// lists them for unhiding, and nobody can hide themselves.
func TestAHiddenPersonsMessagesAreBehindAClickThrough(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "noisy", RealName: "Noisy Neighbour"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cdev", "U2"); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000700, 0).UTC()
	message := domain.Message{ID: "Mnoisy", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U2", Text: "something you would rather not read", CreatedAt: at}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "Enoisy", WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created"}`, CreatedAt: at}, ""); err != nil {
		t.Fatal(err)
	}
	set := func(target, hidden string) int {
		return postForm(t, mux, "/app/people/hidden", url.Values{"_csrf": {auth.CSRFToken("session")}, "target": {target}, "hidden": {hidden}, "return": {"/app?channel=Cdev"}}.Encode(), false).Code
	}
	articleOf := func(page string) string {
		start := strings.Index(page, `data-message-id="Mnoisy"`)
		if start < 0 {
			t.Fatal("the hidden person's message was not delivered at all")
		}
		start = strings.LastIndex(page[:start], "<article")
		end := strings.Index(page[start:], "</article>")
		return page[start : start+end]
	}

	if code := set("U1", "true"); code != http.StatusBadRequest {
		t.Fatalf("hiding yourself answered %d", code)
	}
	if code := set("Unobody", "true"); code != http.StatusNotFound {
		t.Fatalf("hiding a non-member answered %d", code)
	}
	if code := set("U2", "true"); code != http.StatusSeeOther {
		t.Fatalf("hiding answered %d", code)
	}
	page := get(t, mux, "/app?channel=Cdev").Body.String()
	article := articleOf(page)
	requireContains(t, "hidden message", article, "is-hidden-author", `aria-label="Message from a person you have hidden at`, `<details class="hidden-author-reveal"><summary>Message from a person you have hidden. <span class="hidden-author-show">Show message</span></summary></details>`)
	requireContains(t, "privacy list", page, `<ul class="hidden-people"`, `aria-label="Unhide Noisy Neighbour"`)
	if strings.Contains(article, `aria-label="Message from Noisy`) {
		t.Fatal("the hidden person's name labels their message")
	}

	if code := set("U2", "false"); code != http.StatusSeeOther {
		t.Fatalf("unhiding answered %d", code)
	}
	page = get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "unhidden message", articleOf(page), "is-hidden-author")
	requireContains(t, "empty privacy list", page, "hidden anyone.")
	requireMissing(t, "empty privacy list", page, `<ul class="hidden-people"`)
}

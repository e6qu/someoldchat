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
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// TestComposerSendIsIdempotentOnItsClientMessageID covers COMP-01's duplicate
// case: the composer retries a send whose response was lost with the same
// client_msg_id, and the second request must not post a second message.
func TestComposerSendIsIdempotentOnItsClientMessageID(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	form := url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "text": {"exactly once"}, "client_msg_id": {"3f2b8a8e-1c4d-4e2f-9a55-0b6c1d2e3f40"}}.Encode()
	for attempt := 0; attempt < 2; attempt++ {
		if response := postForm(t, mux, "/app/message?channel=Cdev", form, true); response.Code != http.StatusOK {
			t.Fatalf("attempt %d status=%d body=%s", attempt, response.Code, response.Body)
		}
	}
	history, err := s.ListMessages(context.Background(), "Cdev", domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range history.Messages {
		if message.Text == "exactly once" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("a retried send posted %d messages", count)
	}
	// A value that is not a short token is ignored rather than refused: the
	// member never sees the field, so it must not fail their message.
	junk := url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "text": {"still sent"}, "client_msg_id": {"not a token!"}}.Encode()
	if response := postForm(t, mux, "/app/message?channel=Cdev", junk, true); response.Code != http.StatusOK {
		t.Fatalf("junk id status=%d body=%s", response.Code, response.Body)
	}
}

// TestThreadComposerBroadcastsWhenAskedTo covers THREAD-02: the thread
// composer's "Also send to #general" posts the reply as a broadcast, and an
// unchecked box posts an ordinary reply.
func TestThreadComposerBroadcastsWhenAskedTo(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "Mroot", "root", created)
	thread := string(domain.NewMessageTimestamp(created))
	for _, reply := range []struct {
		text      string
		broadcast bool
	}{{"quiet reply", false}, {"loud reply", true}} {
		form := url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "text": {reply.text}, "thread_ts": {thread}}
		if reply.broadcast {
			form.Set("reply_broadcast", "true")
		}
		if response := postForm(t, mux, "/app/message?channel=Cdev&thread="+url.QueryEscape(thread), form.Encode(), true); response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", reply.text, response.Code, response.Body)
		}
	}
	replies, err := (service.Messages{Store: s}).Replies(context.Background(), "T1", "U1", "Cdev", domain.MessageTimestamp(thread), domain.ThreadRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, message := range replies.Messages {
		seen[message.Text] = message.ReplyBroadcast
	}
	if seen["quiet reply"] || !seen["loud reply"] {
		t.Fatalf("broadcast flags = %v", seen)
	}
}

// TestComposerRefusesAnOverLimitMessageSpecifically: the composer used to cut
// a long message at 40,000 characters without saying so. The server now
// names the overrun rather than calling the message empty.
func TestComposerRefusesAnOverLimitMessageSpecifically(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	form := url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "text": {strings.Repeat("x", domain.MaxMessageTextRunes+12)}}.Encode()
	response := postForm(t, mux, "/app/message?channel=Cdev", form, true)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "12 characters too long") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

// TestComposerSchedulesASuggestedTimeWithoutScript covers SCHED-01's no-JS
// path: "Tomorrow at 9:00 AM" submits a preset and a zone, not an instant.
func TestComposerSchedulesASuggestedTimeWithoutScript(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	form := url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "text": {"see you tomorrow"}, "schedule_preset": {"tomorrow"}, "timezone": {"Europe/Paris"}}.Encode()
	response := postForm(t, mux, "/app/message/schedule?channel=Cdev", form, false)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	page, err := (service.Messages{Store: s}).ScheduledMessages(context.Background(), "T1", "U1", "", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("scheduled=%v err=%v", page.Items, err)
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	if due := page.Items[0].PostAt.In(paris); due.Hour() != 9 || due.Minute() != 0 {
		t.Fatalf("scheduled for %v, want 9:00 in the member's zone", due)
	}
}

func TestPresetLocalTimeNamesSlacksSuggestedTimes(t *testing.T) {
	// A Monday: "Monday at 9:00 AM" is next week's Monday, not today.
	monday := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		preset string
		now    time.Time
		want   time.Time
	}{
		{"tomorrow", monday, time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)},
		{"monday", monday, time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)},
		{"monday", monday.AddDate(0, 0, 3), time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)},
		{"1h", monday, monday.Add(time.Hour)},
	} {
		got, ok := presetLocalTime(test.preset, test.now, time.UTC)
		if !ok || !got.Equal(test.want) {
			t.Errorf("%s from %v = %v, want %v", test.preset, test.now, got, test.want)
		}
	}
	if _, ok := presetLocalTime("someday", monday, time.UTC); ok {
		t.Error("an unknown preset resolved to a time")
	}
}

// TestComposerDirectorySuggestsNonMembersAndBroadcastMentions covers COMP-03:
// a workspace member outside the channel is suggested and marked as such, an
// app is marked as one, and @channel/@here carry Slack's descriptions.
// @everyone belongs to the workspace's general channel only.
func TestComposerDirectorySuggestsNonMembersAndBroadcastMentions(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "grace", RealName: "Grace Hopper", Profile: domain.UserProfile{DisplayName: "grace"}})
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "composer directory", body,
		`<template id="composer-directory">`,
		`data-kind="person" data-id="U1" data-name="Ada Developer" data-real="Ada Developer"`,
		`data-kind="special" data-id="&lt;!here&gt;" data-name="here" data-description="Notify everyone online in this channel"`,
		`data-kind="special" data-id="&lt;!channel&gt;" data-name="channel" data-description="Notify everyone in this channel"`,
	)
	grace := body[strings.Index(body, `data-id="U2"`):]
	grace = grace[:strings.Index(grace, "</i>")]
	if strings.Contains(grace, "data-member") {
		t.Fatalf("a non-member is marked as a member: %s", grace)
	}
	if !strings.Contains(body[strings.Index(body, `data-id="U1"`):], "data-member") {
		t.Fatal("the channel member is not marked as one")
	}
	requireMissing(t, "a channel that is not the general channel", body, `data-name="everyone"`)
}

// TestBuiltInSlashCommandsActAndRefuseSpecifically covers APP-05's built-ins:
// each one changes what it names and refuses bad input with a reason the
// member can act on rather than an outage.
func TestBuiltInSlashCommandsActAndRefuseSpecifically(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "grace", RealName: "Grace Hopper"})
	send := func(text string) *httptestResponse {
		response := postForm(t, mux, "/app/message?channel=Cdev", url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "text": {text}}.Encode(), true)
		return &httptestResponse{code: response.Code, body: response.Body.String(), redirect: response.Header().Get("HX-Redirect")}
	}
	if response := send("/topic Release week"); response.code != http.StatusNoContent || !strings.Contains(response.redirect, "notice=") {
		t.Fatalf("/topic status=%d redirect=%q body=%s", response.code, response.redirect, response.body)
	}
	conversation, err := s.GetConversation(context.Background(), "Cdev")
	if err != nil || conversation.Topic != "Release week" {
		t.Fatalf("topic=%q err=%v", conversation.Topic, err)
	}
	if response := send("/invite <@U2>"); response.code != http.StatusNoContent {
		t.Fatalf("/invite status=%d body=%s", response.code, response.body)
	}
	if member, err := (service.Messages{Store: s}).IsConversationMember(context.Background(), "T1", "U2", "Cdev"); err != nil || !member {
		t.Fatalf("invited member=%v err=%v", member, err)
	}
	if response := send("/invite @nobody"); response.code != http.StatusNotFound || !strings.Contains(response.body, "No one in this workspace matches @nobody") {
		t.Fatalf("/invite unknown status=%d body=%s", response.code, response.body)
	}
	if response := send("/dnd forever"); response.code != http.StatusBadRequest || !strings.Contains(response.body, "/dnd 30 minutes") {
		t.Fatalf("/dnd status=%d body=%s", response.code, response.body)
	}
	if response := send("/away"); response.code != http.StatusNoContent {
		t.Fatalf("/away status=%d body=%s", response.code, response.body)
	}
	if user, err := s.GetUser(context.Background(), "U1"); err != nil || user.Presence != domain.PresenceAway {
		t.Fatalf("presence=%v err=%v", user.Presence, err)
	}
	if response := send("/status :tada: Shipping"); response.code != http.StatusNoContent {
		t.Fatalf("/status status=%d body=%s", response.code, response.body)
	}
	if user, _ := s.GetUser(context.Background(), "U1"); user.Profile.StatusEmoji != ":tada:" || user.Profile.StatusText != "Shipping" {
		t.Fatalf("status=%q %q", user.Profile.StatusEmoji, user.Profile.StatusText)
	}
	for _, duration := range []struct {
		text    string
		minutes int64
		ok      bool
	}{{"30 minutes", 30, true}, {"2h", 120, true}, {"1 hour", 60, true}, {"25 hours", 0, false}, {"soon", 0, false}} {
		minutes, ok := commandDuration(duration.text)
		if ok != duration.ok || minutes != duration.minutes {
			t.Errorf("commandDuration(%q) = %d, %v", duration.text, minutes, ok)
		}
	}
}

type httptestResponse struct {
	code     int
	body     string
	redirect string
}

// TestMrkdwnBlockQuotesRender: the composer's Blockquote writes Slack's
// "&gt; " lines, which used to render as a literal ">".
func TestMrkdwnBlockQuotesRender(t *testing.T) {
	rendered := string(renderSlackMrkdwn("before\n&gt; quoted *line*\n&gt; second\nafter\n```\n> not a quote\n```"))
	if !strings.Contains(rendered, `<blockquote>quoted <strong>line</strong><br>second</blockquote>`) {
		t.Fatalf("quote not rendered: %s", rendered)
	}
	if strings.Count(rendered, "<blockquote") != 1 || !strings.Contains(rendered, "&gt; not a quote") {
		t.Fatalf("a fenced line was quoted: %s", rendered)
	}
}

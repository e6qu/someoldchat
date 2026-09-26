package domain

import (
	"regexp"
	"testing"
)

// Every identifier minted in one of Slack's object namespaces has Slack's
// shape: the kind prefix, then upper-case letters and digits only. The
// patterns are the pinned OpenAPI document's where it states one
// (specs/upstream/slack-api-specs), and apps parse `<@U[A-Z0-9]+>` out of text.
func TestSlackNamespaceIdentifiersAreUpperCaseAlphanumerics(t *testing.T) {
	mint := func(value string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, test := range []struct {
		name    string
		pattern string
		id      func() string
	}{
		{"user", `^[UW][A-Z0-9]{8,}$`, func() string { v, err := NewUserID(); return mint(string(v), err) }},
		{"channel", `^[C][A-Z0-9]{8,}$`, func() string { v, err := NewConversationID(); return mint(string(v), err) }},
		{"im", `^[D][A-Z0-9]{8,}$`, func() string { v, err := NewDirectConversationID(ConversationTypeIM); return mint(string(v), err) }},
		{"mpim", `^[CGD][A-Z0-9]{8,}$`, func() string { v, err := NewDirectConversationID(ConversationTypeMPIM); return mint(string(v), err) }},
		{"team", `^[TE][A-Z0-9]{8,}$`, func() string { v, err := NewWorkspaceID(); return mint(string(v), err) }},
		{"app", `^A[A-Z0-9]{1,}$`, func() string { v, err := NewAppID(); return mint(string(v), err) }},
		{"bot", `^B[A-Z0-9]{8,}$`, func() string { v, err := NewBotID(); return mint(string(v), err) }},
		{"file", `^[F][A-Z0-9]{8,}$`, func() string { v, err := NewFileID(); return mint(string(v), err) }},
		{"external upload", `^[F][A-Z0-9]{8,}$`, func() string { v, err := NewExternalUploadID(); return mint(string(v), err) }},
		{"canvas", `^[F][A-Z0-9]{8,}$`, func() string { v, err := NewCanvasID(); return mint(string(v), err) }},
		{"list", `^[F][A-Z0-9]{8,}$`, func() string { v, err := NewListID(); return mint(string(v), err) }},
		{"usergroup", `^S[A-Z0-9]{2,}$`, func() string { v, err := NewUserGroupID(); return mint(string(v), err) }},
		{"reminder", `^Rm[A-Z0-9]{8,}$`, func() string { v, err := NewReminderID(); return mint(string(v), err) }},
		{"scheduled message", `^[Q][A-Z0-9]{8,}$`, func() string { v, err := NewScheduledMessageID(); return mint(string(v), err) }},
		{"profile field", `^Xf[A-Z0-9]{8,}$`, func() string { v, err := NewProfileFieldID(); return mint(string(v), err) }},
		{"barrier", `^B[A-Z0-9]{8,}$`, func() string { v, err := NewBarrierID(); return mint(string(v), err) }},
		{"view", `^V[A-Z0-9]{8,}$`, func() string { v, err := NewViewID(); return mint(string(v), err) }},
		{"event", `^Ev[A-Z0-9]{8,}$`, func() string { v, err := NewEventID(); return mint(string(v), err) }},
		{"call", `^R[A-Z0-9]{8,}$`, func() string { v, err := NewCallID(); return mint(string(v), err) }},
		{"bookmark", `^Bk[A-Z0-9]{8,}$`, func() string { v, err := NewBookmarkID(); return mint(string(v), err) }},
		{"workflow", `^Wf[A-Z0-9]{8,}$`, func() string { v, err := NewWorkflowID(); return mint(string(v), err) }},
		{"trigger", `^Ft[A-Z0-9]{8,}$`, func() string { v, err := NewWorkflowTriggerID(); return mint(string(v), err) }},
		{"workflow execution", `^Wx[A-Z0-9]{8,}$`, func() string { v, err := NewWorkflowRunID(); return mint(string(v), err) }},
		{"function execution", `^Fx[A-Z0-9]{8,}$`, func() string { v, err := NewFunctionExecutionID(); return mint(string(v), err) }},
		{"list item", `^Rec[A-Z0-9]{8,}$`, func() string { v, err := NewListItemID(); return mint(string(v), err) }},
		{"app request", `^R[A-Z0-9]{8,}$`, func() string { v, err := NewAppRequestID(); return mint(string(v), err) }},
	} {
		if id := test.id(); !regexp.MustCompile(test.pattern).MatchString(id) {
			t.Errorf("%s id %q does not match %s", test.name, id, test.pattern)
		}
	}
}

// Identifiers are opaque: an identifier minted before SlackID, with a
// lower-case hex tail, is still recognised wherever text names one.
func TestLegacyLowerCaseIdentifiersStillResolveAsMentions(t *testing.T) {
	mentions := MentionsInMessage("hi <@U0a1b2c3d4e> and <@UABC123>", "")
	if len(mentions.Users) != 2 || mentions.Users[0] != "U0a1b2c3d4e" || mentions.Users[1] != "UABC123" {
		t.Fatalf("mentions = %+v", mentions.Users)
	}
}

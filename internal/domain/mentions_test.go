package domain

import (
	"reflect"
	"testing"
)

func TestMentionedUserGroupsParsesSlackTransportReferences(t *testing.T) {
	got := MentionedUserGroups(
		"hello <!subteam^SSECOND|@support> and <!subteam^SFIRST>",
		`{"text":"duplicate <!subteam^SFIRST> and malformed <!subteam^Sbad> <!subteam^> <!subteam^S THIRD>"}`,
	)
	want := []UserGroupID{"SFIRST", "SSECOND"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MentionedUserGroups() = %v, want %v", got, want)
	}
}

func TestMentionedUsersParsesBareAndExpandedReferences(t *testing.T) {
	got := MentionedUsers(
		"hello <@U2|@Ada> and <@WLEGACY>",
		`{"text":"duplicate <@U2> and malformed <@> <@C1> <@U 3>"}`,
	)
	want := []UserID{"U2", "WLEGACY"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MentionedUsers() = %v, want %v", got, want)
	}
}

func TestMentionsInMessageUsesOnlySlackTextBearingBlockFields(t *testing.T) {
	got := MentionsInMessage(
		"top-level <@UTOP> <!subteam^STOP>",
		`[
			{"type":"section","text":{"type":"mrkdwn","text":"section <@USECTION> <!subteam^SSECTION>"}},
			{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[
				{"type":"user","user_id":"URICH"},
				{"type":"usergroup","usergroup_id":"SRICH"}
			]}]},
			{"type":"actions","elements":[
				{"type":"button","text":{"type":"plain_text","text":"literal <@UPLAIN> <!subteam^SPLAIN>"},"value":"<@UVALUE> <!subteam^SVALUE>"}
			]}
		]`,
	)
	want := MessageMentions{
		Users:      []UserID{"URICH", "USECTION", "UTOP"},
		UserGroups: []UserGroupID{"SRICH", "SSECTION", "STOP"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MentionsInMessage() = %+v, want %+v", got, want)
	}
}

func TestMentionsInMessageIgnoresMalformedBlockJSON(t *testing.T) {
	got := MentionsInMessage("<@UTOP>", `[{"type":"section"`)
	want := MessageMentions{Users: []UserID{"UTOP"}, UserGroups: []UserGroupID{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MentionsInMessage() = %+v, want %+v", got, want)
	}
}

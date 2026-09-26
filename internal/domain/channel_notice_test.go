package domain

import (
	"maps"
	"testing"
)

func TestChannelNoticeFieldsReadBackWhatTheTextWrote(t *testing.T) {
	for _, testCase := range []struct {
		subtype MessageSubtype
		notice  ChannelNotice
		want    map[string]string
	}{
		{MessageSubtypeChannelTopic, ChannelNotice{Topic: "ship: it"}, map[string]string{"topic": "ship: it"}},
		{MessageSubtypeChannelTopic, ChannelNotice{}, map[string]string{"topic": ""}},
		{MessageSubtypeChannelPurpose, ChannelNotice{Purpose: "why"}, map[string]string{"purpose": "why"}},
		{MessageSubtypeChannelPurpose, ChannelNotice{}, map[string]string{"purpose": ""}},
		{MessageSubtypeChannelName, ChannelNotice{OldName: "old", Name: "new"}, map[string]string{"old_name": "old", "name": "new"}},
		{MessageSubtypeChannelJoin, ChannelNotice{}, nil},
		{MessageSubtypeChannelLeave, ChannelNotice{}, nil},
	} {
		text, ok := ChannelNoticeText(testCase.subtype, "U1", testCase.notice)
		if !ok {
			t.Fatalf("%s wrote no text", testCase.subtype)
		}
		got := ChannelNoticeFields(Message{AuthorID: "U1", Subtype: testCase.subtype, Text: text})
		if !maps.Equal(got, testCase.want) {
			t.Fatalf("%s %q read back %v, want %v", testCase.subtype, text, got, testCase.want)
		}
	}
	if _, ok := ChannelNoticeText(MessageSubtypeMeMessage, "U1", ChannelNotice{}); ok {
		t.Fatal("a /me message is not a channel notice")
	}
	// A rename stored before the old name was recorded still reports the name.
	legacy := Message{AuthorID: "U1", Subtype: MessageSubtypeChannelName, Text: "<@U1> renamed the channel to launch"}
	if got := ChannelNoticeFields(legacy); !maps.Equal(got, map[string]string{"name": "launch"}) {
		t.Fatalf("legacy rename read back %v", got)
	}
	// Someone else's words are not a notice, whatever the subtype says.
	if got := ChannelNoticeFields(Message{AuthorID: "U2", Subtype: MessageSubtypeChannelTopic, Text: "<@U1> set the channel topic: forged"}); got != nil {
		t.Fatalf("a notice attributed to another author read back %v", got)
	}
}

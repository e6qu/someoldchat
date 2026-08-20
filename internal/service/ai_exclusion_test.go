package service

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

func aiExclusionEvent(id string) events.Event {
	return events.Event{ID: domain.EventID(id), WorkspaceID: "T1", Topic: "channel.ai_exclusion_set"}
}

func conversationsOf(messages []domain.Message) map[domain.ConversationID]bool {
	present := make(map[domain.ConversationID]bool, len(messages))
	for _, message := range messages {
		present[message.Conversation] = true
	}
	return present
}

// TestThreadSummariesRespectAIExclusion proves the exclusion an administrator
// sets actually governs the one AI surface a thread summary reaches: a channel
// kept out of Slack AI shows no summaries, and removing the exclusion brings them
// back. Before this, the flag was set and reported but never consulted, so an
// excluded channel was summarised exactly like any other.
func TestThreadSummariesRespectAIExclusion(t *testing.T) {
	ctx, repository, messages, root := assistantWorld(t)
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "here is a reply", root, ""); err != nil {
		t.Fatal(err)
	}

	before, err := messages.ThreadSummaries(ctx, "T1", "U1", "C1", []domain.MessageTimestamp{root})
	if err != nil {
		t.Fatal(err)
	}
	if summary, ok := before[root]; !ok || summary.ReplyCount < 1 {
		t.Fatalf("thread summary before exclusion = %+v, want a reply counted", before)
	}

	if err := repository.SetConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C1"}, true, aiExclusionEvent("E-exclude")); err != nil {
		t.Fatal(err)
	}
	after, err := messages.ThreadSummaries(ctx, "T1", "U1", "C1", []domain.MessageTimestamp{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("an AI-excluded channel returned %d summaries, want none", len(after))
	}

	if err := repository.SetConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C1"}, false, aiExclusionEvent("E-include")); err != nil {
		t.Fatal(err)
	}
	restored, err := messages.ThreadSummaries(ctx, "T1", "U1", "C1", []domain.MessageTimestamp{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restored[root]; !ok {
		t.Fatalf("removing the exclusion did not restore the summary: %+v", restored)
	}
}

// TestAssistantSearchContextRespectsAIExclusion proves an assistant may not quote
// a channel the workspace has kept out of Slack AI, even to a member who can read
// it: the excluded channel's messages are dropped from the context the assistant
// is handed, while every other channel the member can read stays.
func TestAssistantSearchContextRespectsAIExclusion(t *testing.T) {
	ctx, repository, messages, _ := assistantWorld(t) // C1 carries "how do I deploy?"
	repository.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "ops"})
	repository.SeedConversationMember("C2", "U1")
	if _, err := messages.Post(ctx, "T1", "U1", "C2", "how do I deploy the worker", "", ""); err != nil {
		t.Fatal(err)
	}

	before, err := messages.AssistantSearchContext(ctx, "T1", "U1", "deploy", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if channels := conversationsOf(before.Messages); !channels["C1"] || !channels["C2"] {
		t.Fatalf("assistant context before exclusion missed a channel: %v", channels)
	}

	if err := repository.SetConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C2"}, true, aiExclusionEvent("E-exclude")); err != nil {
		t.Fatal(err)
	}
	after, err := messages.AssistantSearchContext(ctx, "T1", "U1", "deploy", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	channels := conversationsOf(after.Messages)
	if channels["C2"] {
		t.Fatalf("an AI-excluded channel was quoted to the assistant: %v", channels)
	}
	if !channels["C1"] {
		t.Fatalf("excluding C2 also dropped C1: %v", channels)
	}
}

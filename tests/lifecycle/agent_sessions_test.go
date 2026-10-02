package lifecycle

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// agentSessionMachine is AgentSessionStatus as the agents.sessions.setStatus
// reference page defines it: four statuses an agent writes, with no move the
// page forbids and none it calls final. closed means the agent "will no
// longer respond on it", which is the agent's statement about itself rather
// than a state the page stops it leaving.
func agentSessionMachine() machine {
	every := func(except string) []string {
		var targets []string
		for _, status := range []string{"active", "processing", "suspended", "closed"} {
			if status != except {
				targets = append(targets, status)
			}
		}
		return targets
	}
	return machine{
		terminal: []string{},
		transitions: map[string][]string{
			"active": every("active"), "processing": every("processing"),
			"suspended": every("suspended"), "closed": every("closed"),
		},
		why: "an agent's status is what the agent says it is doing; the reference names no forbidden move and no final status, and a member's stop deliberately leaves the status for the app to move",
	}
}

// agentSessionDriver drives one agent's status through
// agents.sessions.setStatus on a session it created.
func agentSessionDriver() driver {
	const (
		workspace = domain.WorkspaceID("T1")
		app       = domain.AppID("A1")
		bot       = domain.UserID("U-bot")
		channel   = domain.ConversationID("C1")
	)
	var thread domain.MessageTimestamp
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			ctx := context.Background()
			repository := memory.New()
			repository.SeedWorkspace(domain.Workspace{ID: workspace, Name: "test"})
			repository.SeedUser(domain.User{ID: "U-member", WorkspaceID: workspace, Name: "member"})
			repository.SeedUser(domain.User{ID: bot, WorkspaceID: workspace, Name: "agent"})
			repository.SeedConversation(domain.Conversation{ID: channel, WorkspaceID: workspace, Name: "general"})
			repository.SeedConversationMember(channel, "U-member")
			repository.SeedConversationMember(channel, bot)
			messages := service.Messages{Store: repository}
			root, err := messages.Post(ctx, workspace, "U-member", channel, "a task", "", "")
			if err != nil {
				t.Fatal(err)
			}
			thread = domain.NewMessageTimestamp(root.CreatedAt)
			if _, err := messages.SetAgentSessionStatus(ctx, workspace, bot, app, channel, thread, domain.AgentSessionStatusRequest{Status: domain.AgentSessionStatus(state)}); err != nil {
				t.Fatal(err)
			}
			return messages, true
		},
		attempt: func(messages service.Messages, _, to string) error {
			_, err := messages.SetAgentSessionStatus(context.Background(), workspace, bot, app, channel, thread, domain.AgentSessionStatusRequest{Status: domain.AgentSessionStatus(to)})
			return err
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			session, err := messages.Store.GetAgentSession(context.Background(), workspace, channel, thread)
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := session.Agent(app)
			return string(agent.Status)
		},
	}
}

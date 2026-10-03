package service

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Slackbot's custom responses and its answers to direct messages.

// SlackbotResponses is the workspace's custom responses, oldest first, for any
// member.
func (m Messages) SlackbotResponses(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.SlackbotResponse, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	return m.Store.ListSlackbotResponses(ctx, workspaceID)
}

// AddSlackbotResponse adds a custom response. Any full member may, as Slack's
// default lets everyone in a workspace customize Slackbot; a guest may not.
func (m Messages) AddSlackbotResponse(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggers, replies []string) (domain.SlackbotResponse, error) {
	if err := m.refuseGuest(ctx, workspaceID, userID); err != nil {
		return domain.SlackbotResponse{}, err
	}
	triggers, replies, err := domain.NormalizeSlackbotResponse(triggers, replies)
	if err != nil {
		return domain.SlackbotResponse{}, err
	}
	id, err := domain.NewSlackbotResponseID()
	if err != nil {
		return domain.SlackbotResponse{}, err
	}
	value := domain.SlackbotResponse{WorkspaceID: workspaceID, ID: id, Triggers: triggers, Replies: replies, CreatedBy: userID, CreatedAt: time.Now().UTC()}
	event, err := newEvent(workspaceID, userID, events.NewPayload(events.SlackbotResponseCreatedTopic, events.String("response_id", string(id))), value.CreatedAt)
	if err != nil {
		return domain.SlackbotResponse{}, err
	}
	if err := m.Store.CreateSlackbotResponse(ctx, value, event); err != nil {
		return domain.SlackbotResponse{}, err
	}
	return value, nil
}

// DeleteSlackbotResponse removes a custom response, for any full member.
func (m Messages) DeleteSlackbotResponse(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SlackbotResponseID) error {
	if err := m.refuseGuest(ctx, workspaceID, userID); err != nil {
		return err
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload(events.SlackbotResponseDeletedTopic, events.String("response_id", string(id))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteSlackbotResponse(ctx, workspaceID, id, event)
}

// DispatchSlackbotResponses answers the messages members posted after the
// cursor: a message that says a custom response's phrase gets one of its
// replies where it was posted, and a direct message to Slackbot that matches
// none gets help. Each answer is keyed to the message's event, so reading the
// same events again answers nothing twice. A message that can no longer be
// answered — its author has left the conversation or the workspace, or the
// conversation was archived — is passed over; any other failure stops the
// batch before the cursor moves, so the next cycle retries.
func (m Messages) DispatchSlackbotResponses(ctx context.Context, workspaceID domain.WorkspaceID, limit int) (int, error) {
	if limit <= 0 {
		return 0, store.InvalidArgument("Slackbot response dispatch limit must be positive")
	}
	cursor, err := m.Store.SlackbotResponseCursor(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	records, err := m.Store.ListEventsAfter(ctx, workspaceID, cursor, limit)
	if err != nil {
		return 0, err
	}
	responses := map[domain.WorkspaceID][]domain.SlackbotResponse{}
	answered := 0
	processed := cursor
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return answered, err
		}
		post, ok, err := m.slackbotAnswer(ctx, record, responses)
		if err != nil {
			return answered, err
		}
		if ok {
			_, err := m.PostAsSlackbot(ctx, record.Event.WorkspaceID, post.member, post.SlackbotPost)
			switch {
			case err == nil:
				answered++
			case errors.Is(err, store.ErrNotFound), errors.Is(err, domain.ErrNotInConversation), errors.Is(err, domain.ErrConversationAlreadyArchived):
				// Past answering; see above.
			default:
				if advanceErr := m.Store.AdvanceSlackbotResponseCursor(ctx, workspaceID, processed); advanceErr != nil {
					return answered, errors.Join(err, advanceErr)
				}
				return answered, err
			}
		}
		processed = record.Sequence
	}
	if processed != cursor {
		if err := m.Store.AdvanceSlackbotResponseCursor(ctx, workspaceID, processed); err != nil {
			return answered, err
		}
	}
	return answered, nil
}

// slackbotReply is an answer Slackbot gives, for the member it answers.
type slackbotReply struct {
	domain.SlackbotPost
	member domain.UserID
}

// slackbotAnswer decides what Slackbot says to one journal record, if
// anything: only a member's own new message is answered, never an app's, a
// bot's or Slackbot's, and never a system message.
func (m Messages) slackbotAnswer(ctx context.Context, record events.Record, responses map[domain.WorkspaceID][]domain.SlackbotResponse) (slackbotReply, bool, error) {
	if record.Event.Topic != "message.created" {
		return slackbotReply{}, false, nil
	}
	snapshot, ok, err := decodeMessageEventSnapshot(record.Event)
	if err != nil || !ok {
		// A record without a readable snapshot cannot be answered; it is
		// passed over rather than stopping Slackbot for every later message.
		return slackbotReply{}, false, nil
	}
	message := snapshot.Current
	if message.AuthorID == "" || message.AuthorID == domain.SlackbotUserID || message.AppID != "" || message.PostingBot() != "" ||
		message.Subtype != "" || message.Deleted || message.Text == "" {
		return slackbotReply{}, false, nil
	}
	workspace := message.WorkspaceID
	list, cached := responses[workspace]
	if !cached {
		if list, err = m.Store.ListSlackbotResponses(ctx, workspace); err != nil {
			return slackbotReply{}, false, err
		}
		responses[workspace] = list
	}
	reply := domain.SlackbotPost{
		Conversation: message.Conversation, ThreadTimestamp: message.ThreadTimestamp,
		IdempotencyKey: "slackbot-response:" + string(record.Event.ID),
	}
	for _, response := range list {
		if response.Matches(message.Text) {
			reply.Text = response.Reply(string(record.Event.ID))
			return slackbotReply{SlackbotPost: reply, member: message.AuthorID}, true, nil
		}
	}
	// A direct message to Slackbot that no custom response matches is
	// answered with help.
	conversation, err := m.Store.GetConversation(ctx, message.Conversation)
	if errors.Is(err, store.ErrNotFound) {
		return slackbotReply{}, false, nil
	}
	if err != nil {
		return slackbotReply{}, false, err
	}
	if !conversation.IsDirectOrGroup() || conversation.Kind == domain.ConversationTypeMPIM {
		return slackbotReply{}, false, nil
	}
	withSlackbot, err := m.Store.IsConversationMember(ctx, message.Conversation, domain.SlackbotUserID)
	if err != nil || !withSlackbot {
		return slackbotReply{}, false, err
	}
	reply.Text = domain.SlackbotHelpText
	return slackbotReply{SlackbotPost: reply, member: message.AuthorID}, true, nil
}

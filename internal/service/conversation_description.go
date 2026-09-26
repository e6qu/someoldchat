package service

import (
	"context"
	"slices"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// describeConversation fills the facts about a conversation that depend on the
// reader or on the workspace rather than on the stored row: whether the reader
// belongs to it, how many people do, whether it is a required channel, and for a
// DM who it is with. Every service method that hands a conversation to a caller
// goes through here or through describeConversations, so the Web API reports
// the same object for conversations.info, conversations.create and the rest.
//
// A conversation read straight from a store carries none of these; is_member
// used to be the literal true for every conversation, including the public
// channels a reader had never joined.
func (m Messages) describeConversation(ctx context.Context, viewer domain.UserID, conversation domain.Conversation) (domain.Conversation, error) {
	member, err := m.Store.IsConversationMember(ctx, conversation.ID, viewer)
	if err != nil {
		return domain.Conversation{}, err
	}
	count, err := m.Store.CountConversationMembers(ctx, conversation.ID)
	if err != nil {
		return domain.Conversation{}, err
	}
	conversation.IsMember, conversation.NumMembers = member, count
	values := []domain.Conversation{conversation}
	if err := m.describeWorkspaceFacts(ctx, viewer, values); err != nil {
		return domain.Conversation{}, err
	}
	return values[0], nil
}

// described adapts describeConversation to a (conversation, error) result, so a
// method that returns a store's answer can describe it in the return statement.
func (m Messages) described(ctx context.Context, viewer domain.UserID) func(domain.Conversation, error) (domain.Conversation, error) {
	return func(conversation domain.Conversation, err error) (domain.Conversation, error) {
		if err != nil {
			return domain.Conversation{}, err
		}
		return m.describeConversation(ctx, viewer, conversation)
	}
}

// describeConversations is describeConversation for a store listing, which
// already carries IsMember and NumMembers per row: the remaining facts cost one
// workspace read for the page and one participant read per direct conversation.
func (m Messages) describeConversations(ctx context.Context, viewer domain.UserID, conversations []domain.Conversation) error {
	return m.describeWorkspaceFacts(ctx, viewer, conversations)
}

func (m Messages) describeWorkspaceFacts(ctx context.Context, viewer domain.UserID, conversations []domain.Conversation) error {
	required := map[domain.WorkspaceID][]domain.ConversationID{}
	for index := range conversations {
		conversation := &conversations[index]
		if !conversation.IsDirectOrGroup() {
			defaults, known := required[conversation.WorkspaceID]
			if !known {
				workspace, err := m.Store.GetWorkspace(ctx, conversation.WorkspaceID)
				if err != nil {
					return err
				}
				defaults = workspace.DefaultChannelIDs
				required[conversation.WorkspaceID] = defaults
			}
			conversation.IsGeneral = slices.Contains(defaults, conversation.ID)
			continue
		}
		if err := m.describeDirect(ctx, viewer, conversation); err != nil {
			return err
		}
	}
	return nil
}

// describeDirect names who a DM is with. For a one-to-one that is the other
// participant, or the reader for a self-DM; for a group DM it is Slack's
// mpdm-alice--bob--carol-1 handle.
func (m Messages) describeDirect(ctx context.Context, viewer domain.UserID, conversation *domain.Conversation) error {
	participants, err := m.Store.DirectParticipants(ctx, conversation.ID)
	if err != nil {
		return err
	}
	if conversation.Kind == domain.ConversationTypeIM {
		conversation.DirectUserID = viewer
		for _, participant := range participants {
			if participant != viewer {
				conversation.DirectUserID = participant
				break
			}
		}
		if conversation.DirectUserID == "" {
			return nil
		}
		user, err := m.Store.GetUser(ctx, conversation.DirectUserID)
		if err != nil {
			return err
		}
		conversation.DirectUserDeleted = user.Deleted
		return nil
	}
	names := make([]string, 0, len(participants))
	for _, participant := range participants {
		user, err := m.Store.GetUser(ctx, participant)
		if err != nil {
			return err
		}
		names = append(names, user.Name)
	}
	conversation.GroupDirectHandle = GroupDirectHandle(names)
	return nil
}

// GroupDirectHandle is the name Slack gives a group DM: its participants'
// usernames, sorted, joined by a double dash, with the mpdm- prefix and the -1
// suffix Slack uses for the first group DM between a given set of people.
func GroupDirectHandle(usernames []string) string {
	sorted := slices.Clone(usernames)
	slices.Sort(sorted)
	return "mpdm-" + strings.Join(sorted, "--") + "-1"
}

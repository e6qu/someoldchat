package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// HUDDLE-01 forbids fabricating a connected state, and this deployment carries
// no audio or video transport at all. What it does carry is the rest of a
// huddle, durably: one running huddle per conversation, who is in it, join,
// leave, and end for everyone. The surface says which of those two it is
// wherever it offers a control, so nobody presses "Join huddle" expecting
// sound.
//
// The alternative — hiding the surface until WebRTC exists — would leave the
// lifecycle unreachable and untestable, and would not make the missing media
// any more present.

type huddleView struct {
	// Visible is false when the reader cannot be in this conversation's
	// huddle at all, so the bar is absent rather than showing a control that
	// would be refused.
	Visible      bool
	Active       bool
	Joined       bool
	CanEnd       bool
	ChannelName  string
	CSRFToken    string
	Notice       string
	Participants []string
	StartURL     string
	JoinURL      string
	LeaveURL     string
	EndURL       string
}

func huddleActionURL(action, channel string) string {
	return "/app/huddle/" + action + "?channel=" + url.QueryEscape(channel)
}

// huddleFor builds the bar for one conversation. A failure to read the huddle
// is reported as an absent bar rather than a failed page: the conversation is
// still readable, and a timeline that will not render because presence is
// unavailable is a worse outcome than a missing control.
func (h Handler) huddleFor(ctx context.Context, principal auth.Principal, conversation domain.Conversation, csrfToken, notice string, names *userNames) huddleView {
	if conversation.ID == "" || conversation.Archived {
		return huddleView{}
	}
	view := huddleView{
		Visible: true, ChannelName: conversationName(conversation), CSRFToken: csrfToken, Notice: notice,
		StartURL: huddleActionURL("start", string(conversation.ID)),
		JoinURL:  huddleActionURL("join", string(conversation.ID)),
		LeaveURL: huddleActionURL("leave", string(conversation.ID)),
		EndURL:   huddleActionURL("end", string(conversation.ID)),
	}
	call, err := h.Messages.ActiveHuddle(ctx, principal.WorkspaceID, principal.UserID, conversation.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			// Not a member, or the read failed: either way there is no bar.
			return huddleView{}
		}
		return view
	}
	view.Active = true
	view.CanEnd = call.CreatedBy == principal.UserID
	for _, participant := range call.Participants {
		if participant == principal.UserID {
			view.Joined = true
		}
		view.Participants = append(view.Participants, names.name(participant))
	}
	return view
}

func (h Handler) huddleFragment(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeHuddleFragment(w, huddleView{})
		return
	}
	view := h.huddleFor(r.Context(), principal, conversation, auth.CSRFToken(sessionCookie.Value),
		strings.TrimSpace(r.URL.Query().Get("notice")), h.newUserNames(r.Context(), principal))
	h.writeHuddleFragment(w, view)
}

func (h Handler) writeHuddleFragment(w http.ResponseWriter, view huddleView) {
	h.writePartial(w, "huddle", view, "the huddle could not be rendered")
}

// huddleMutation is the shared shape of start, join, leave and end: each
// authorizes, decodes, applies one service call and returns to the
// conversation. They differ only in the call and in what the notice says.
func (h Handler) huddleMutation(name string, apply func(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.Call, error), notice string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
		if err != nil {
			h.writeAuthError(w, r, err)
			return
		}
		if _, ok := h.decodeMutation(w, r, "Reload the page and try again."); !ok {
			return
		}
		channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
		if channel == "" {
			h.writeMutationError(w, r, http.StatusBadRequest, "The huddle did not change", "Open a conversation and try again.")
			return
		}
		if _, err := apply(r.Context(), principal.WorkspaceID, principal.UserID, channel); err != nil {
			h.writeHuddleError(w, r, err, name)
			return
		}
		h.redirectMutation(w, r, "/app?channel="+url.QueryEscape(string(channel))+"&notice="+url.QueryEscape(notice))
	}
}

func (h Handler) writeHuddleError(w http.ResponseWriter, r *http.Request, err error, action string) {
	switch {
	case errors.Is(err, service.ErrHuddleNotOwned):
		h.writeMutationError(w, r, http.StatusForbidden, "The huddle was not ended",
			"Only the person who started a huddle, or a workspace administrator, can end it for everyone. You can leave it instead.")
	case errors.Is(err, store.ErrNotFound):
		h.writeMutationError(w, r, http.StatusNotFound, "There is no huddle here",
			"It may have ended while this page was open. Reload to see the conversation as it is now.")
	case errors.Is(err, store.ErrConflict):
		h.writeMutationError(w, r, http.StatusConflict, "That huddle has ended",
			"Start a new one if you still want to talk.")
	case errors.Is(err, service.ErrNotInConversation):
		h.writeMutationError(w, r, http.StatusForbidden, "You are not in this conversation",
			"A huddle belongs to its conversation, so joining one means being in it.")
	default:
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "The huddle could not be "+action,
			"Nothing was changed. Try again in a moment.")
	}
}

// The four mutations are named methods rather than closures over the seam so
// the route table reads as a list of operations.
func (h Handler) startHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return h.Messages.StartHuddle(ctx, workspaceID, userID, conversationID, "")
}

func (h Handler) joinHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return h.Messages.JoinHuddle(ctx, workspaceID, userID, conversationID)
}

func (h Handler) leaveHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return h.Messages.LeaveHuddle(ctx, workspaceID, userID, conversationID)
}

func (h Handler) endHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return h.Messages.EndHuddle(ctx, workspaceID, userID, conversationID)
}

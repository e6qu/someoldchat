package web

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/huddlesfu"
)

// huddleEventAppender is the slice of the store the SFU needs: a way to inject a
// recipient-scoped realtime event. The server-sent event stream the huddle
// already listens on carries it to the participant's browser.
type huddleEventAppender interface {
	AppendEvent(context.Context, events.Event) error
}

// HuddleSignalEmitter turns an SFU server→browser signal into a recipient-scoped
// huddle.signal event, marked as coming from the SFU rather than a peer. It
// implements huddlesfu.Emitter.
type HuddleSignalEmitter struct {
	Store huddleEventAppender
}

// EmitHuddleSignal appends the event that delivers one SFU offer or ICE
// candidate to one participant's browser.
func (e HuddleSignalEmitter) EmitHuddleSignal(ctx context.Context, workspaceID, callID, recipient string, signal huddlesfu.Signal) error {
	payload, err := json.Marshal(signal)
	if err != nil {
		return err
	}
	id, err := domain.NewEventID()
	if err != nil {
		return err
	}
	// The actor is the recipient because the event is theirs to receive; the
	// from_user_id marks it as the SFU's, which is how the browser tells an SFU
	// negotiation message apart from anything a member sent.
	event, err := events.New(id, domain.WorkspaceID(workspaceID), domain.UserID(recipient), events.NewPayload("huddle.signal",
		events.String("call_id", callID),
		events.String("user_id", recipient),
		events.String("from_user_id", huddleSFUActor),
		events.String("signal", signal.Kind),
		events.String("payload", string(payload)),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	return e.Store.AppendEvent(ctx, event)
}

// huddleSFUActor is the from_user_id the SFU stamps on its signals. No member
// has this id, so a browser can trust that a signal carrying it is the server's.
const huddleSFUActor = "sfu"

// huddleSFUOffer accepts a participant's initial publish offer and returns the
// SFU's answer. From here the browser holds a single connection to this process
// and the SFU forwards everyone else's media to it.
func (h Handler) huddleSFUOffer(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if h.SFU == nil {
		http.Error(w, "Huddle media is not available on this server.", http.StatusServiceUnavailable)
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		return
	}
	callID := domain.CallID(strings.TrimSpace(fields["call_id"]))
	offer := fields["sdp"]
	if callID == "" || strings.TrimSpace(offer) == "" {
		http.Error(w, "A call id and an offer are required.", http.StatusBadRequest)
		return
	}
	if !h.callAdmitsParticipant(r.Context(), principal, callID) {
		http.Error(w, "You are not in this huddle.", http.StatusForbidden)
		return
	}
	answer, err := h.SFU.Offer(string(principal.WorkspaceID), string(callID), string(principal.UserID), offer)
	if err != nil {
		http.Error(w, "The huddle media connection could not be established.", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]string{"sdp": answer})
}

// huddleSFUSignal feeds a participant's answer to an SFU renegotiation offer, or
// a trickled ICE candidate, to their connection.
func (h Handler) huddleSFUSignal(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if h.SFU == nil {
		http.Error(w, "Huddle media is not available on this server.", http.StatusServiceUnavailable)
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		return
	}
	callID := strings.TrimSpace(fields["call_id"])
	kind := strings.TrimSpace(fields["kind"])
	if callID == "" || (kind != "answer" && kind != "candidate") {
		http.Error(w, "A call id and a valid signal are required.", http.StatusBadRequest)
		return
	}
	_ = h.SFU.Signal(callID, string(principal.UserID), huddlesfu.Signal{
		Kind: kind, SDP: fields["sdp"], Candidate: fields["candidate"],
	})
	w.WriteHeader(http.StatusNoContent)
}

// huddlePresence broadcasts one participant's live microphone, camera and
// screen-share state to everyone in the huddle, so their tiles can show a muted
// or camera-off badge and promote a sharer to a presenter view. It is the same
// standing as a reaction: the sender must be a live participant.
func (h Handler) huddlePresence(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if h.HuddleStore == nil {
		http.Error(w, "Huddle media is not available on this server.", http.StatusServiceUnavailable)
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		return
	}
	callID := domain.CallID(strings.TrimSpace(fields["call_id"]))
	if callID == "" {
		http.Error(w, "A call id is required.", http.StatusBadRequest)
		return
	}
	call, err := h.Messages.GetCall(r.Context(), principal.WorkspaceID, principal.UserID, callID)
	if err != nil || !call.EndedAt.IsZero() || !slices.Contains(call.Participants, principal.UserID) {
		http.Error(w, "You are not in this huddle.", http.StatusForbidden)
		return
	}
	id, err := domain.NewEventID()
	if err != nil {
		http.Error(w, "The presence update could not be sent.", http.StatusInternalServerError)
		return
	}
	event, err := events.New(id, principal.WorkspaceID, principal.UserID, events.NewPayload("huddle.presence",
		events.String("call_id", string(callID)),
		events.String("channel_id", string(call.ConversationID)),
		events.String("user_id", string(principal.UserID)),
		events.String("muted", huddleBool(fields["muted"])),
		events.String("camera", huddleBool(fields["camera"])),
		events.String("presenting", huddleBool(fields["presenting"])),
	), time.Now().UTC())
	if err == nil {
		_ = h.HuddleStore.AppendEvent(r.Context(), event)
	}
	w.WriteHeader(http.StatusNoContent)
}

// huddleBool normalises a form flag to "true" or "false" for the presence
// payload, so the browser reads a stable value.
func huddleBool(value string) string {
	if strings.TrimSpace(value) == "true" {
		return "true"
	}
	return "false"
}

// callAdmitsParticipant reports whether the principal is a live participant of
// the call, so a stranger cannot publish into or subscribe to a huddle they are
// not in.
func (h Handler) callAdmitsParticipant(ctx context.Context, principal auth.Principal, callID domain.CallID) bool {
	call, err := h.Messages.GetCall(ctx, principal.WorkspaceID, principal.UserID, callID)
	if err != nil {
		return false
	}
	return call.EndedAt.IsZero() && slices.Contains(call.Participants, principal.UserID)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

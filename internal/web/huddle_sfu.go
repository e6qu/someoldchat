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

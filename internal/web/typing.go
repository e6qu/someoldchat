package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The typing indicator, as the reader sees it.
//
// Slack names the people it can and stops naming them when there are too many
// to read at a glance: one name, two names, then a count. The wording is built
// here rather than in the browser so it has one owner — the same place that
// resolves a member id to the name this reader is allowed to see.
//
// The line is deliberately not part of the timeline fragment. The timeline is
// refreshed by a shared generation-and-abort machine that exists to keep
// message history consistent, and folding a signal that changes every couple of
// seconds into it would mean re-fetching the whole conversation to render six
// words, and would give the indicator the timeline's failure modes as well.

// typingTimingLiteral hands the client the two durations the server already
// decided, rather than letting the browser keep its own copy of them. The
// renewal interval has to stay inside the signal's lifetime or a steady typist
// blinks; that relationship is stated once, in the domain, and a second
// hand-written copy in a script is exactly how it would drift.
func typingTimingLiteral() string {
	return fmt.Sprintf("{ttl:%d,interval:%d}", domain.TypingSignalTTL.Milliseconds(), domain.TypingSignalInterval.Milliseconds())
}

type typingView struct {
	// Text is the whole sentence, already assembled. An empty Text renders
	// nothing at all rather than an empty live region that a screen reader
	// would announce as a change.
	Text string
}

// typingSentence is Slack's own escalation. Naming everyone stops being useful
// somewhere around three people — the line grows past the width it is given and
// starts reflowing the composer — so the fourth typist turns it into a count.
func typingSentence(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0] + " is typing…"
	case 2:
		return names[0] + " and " + names[1] + " are typing…"
	case 3:
		return names[0] + ", " + names[1] + " and " + names[2] + " are typing…"
	default:
		return "Several people are typing…"
	}
}

func (h Handler) typingFragment(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	if channel == "" {
		h.writeTypingFragment(w, typingView{})
		return
	}
	signals, err := h.Messages.TypingIn(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		// A conversation nobody can read is answered with an empty line rather
		// than an error: the reader has lost nothing they had, and the client
		// polls this often enough that an error page would be a recurring one.
		h.writeTypingFragment(w, typingView{})
		return
	}
	names := h.newUserNames(r.Context(), principal)
	people := make([]string, 0, len(signals))
	for _, signal := range signals {
		people = append(people, names.name(signal.UserID))
	}
	h.writeTypingFragment(w, typingView{Text: typingSentence(people)})
}

func (h Handler) writeTypingFragment(w http.ResponseWriter, view typingView) {
	h.writePartial(w, "typing", view, "the typing indicator could not be rendered")
}

// recordTyping is the composer's half. Like the presence heartbeat it answers
// 204 and nothing else: the client sends it in the background every few seconds
// while someone is composing and has no use for a body.
func (h Handler) recordTyping(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The typing signal could not be read."); !ok {
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	if channel != "" {
		// A refused signal costs the reader an indicator and nothing else — the
		// commonest cause is typing in a conversation one has just been removed
		// from — so it is not worth an error page in front of a composer that
		// is otherwise working.
		_ = h.Messages.SetTyping(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	}
	w.WriteHeader(http.StatusNoContent)
}

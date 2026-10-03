package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A member who cannot invite directly gets Slack's Add coworkers as a request
// to the workspace administrators: the sidebar opens the request dialog, the
// fallback page carries the same form, and sending it leaves a pending
// request naming the member, with a notice that says it awaits approval.
func TestAMemberRequestsAnInvitationFromTheSidebar(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	home := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "Add coworkers", home,
		`data-dialog-open="invite-request-dialog"`,
		`id="invite-request-dialog"`,
		`action="/app/invitations/request?channel=Cdev"`,
	)
	if strings.Contains(home, `<a class="side-add" href="/app/admin/auth">`) {
		t.Fatal("a member's Add coworkers still leads to administration")
	}
	requireContains(t, "fallback page", get(t, mux, "/app/invitations/request?channel=Cdev").Body.String(), `name="email"`, "Send request")

	refused := postForm(t, mux, "/app/invitations/request?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "email": {"not an address"},
	}.Encode(), false)
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("an invalid address answered %d", refused.Code)
	}

	sent := postForm(t, mux, "/app/invitations/request?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "email": {" Friend@Example.com "}, "reason": {" Joining the platform team "},
	}.Encode(), false)
	if sent.Code != http.StatusSeeOther || !strings.Contains(sent.Header().Get("Location"), url.QueryEscape("was sent to your workspace administrators")) {
		t.Fatalf("send answered %d to %q", sent.Code, sent.Header().Get("Location"))
	}
	pending, err := s.ListInviteRequests(context.Background(), "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	if err != nil || len(pending.Requests) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	request := pending.Requests[0]
	if request.Email != "friend@example.com" || request.RequestedBy != "U1" || request.CustomMessage != "Joining the platform team" {
		t.Fatalf("request=%+v", request)
	}
}

package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// A response_url answers like Slack's: a body that can never be applied is a
// 400 that does not spend one of the URL's uses, a URL that cannot be used
// any more is a 404 naming why, and no handled refusal is a 5xx. Invalid JSON
// used to spend a use and answer 404; a handled service refusal answered 503.
func TestAppResponseURLAnswersSlackStatusesWithoutSpendingUsesOnBadBodies(t *testing.T) {
	s, mux := browserWorkspace(t, nil)
	now := time.Now().UTC()
	if err := s.CreateAppInteractionCapabilities(context.Background(),
		domain.AppTrigger{TokenHash: domain.HashToken("trigger"), AppID: "A1", WorkspaceID: "T1", UserID: "U1", CreatedAt: now, ExpiresAt: now.Add(time.Minute)},
		domain.AppResponseURL{TokenHash: domain.HashToken("five-uses"), AppID: "A1", WorkspaceID: "T1", UserID: "U1", ConversationID: "Cdev", CreatedAt: now, ExpiresAt: now.Add(time.Minute), UsesRemaining: 5},
	); err != nil {
		t.Fatal(err)
	}
	post := func(token, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/app-response/"+token, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	for _, test := range []struct {
		body   string
		status int
		reason string
	}{
		{`not json`, http.StatusBadRequest, "invalid_payload"},
		{`{"text":`, http.StatusBadRequest, "invalid_payload"},
		{`{}`, http.StatusBadRequest, "no_text"},
	} {
		response := post("five-uses", test.body)
		if response.Code != test.status || response.Body.String() != test.reason {
			t.Fatalf("body %q status=%d reason=%q, want %d %q", test.body, response.Code, response.Body, test.status, test.reason)
		}
	}
	// All five uses survived the refusals above: these requests spend them.
	// The app has no bot in this fixture, so the destination is what fails,
	// and that is a 404, not a 5xx.
	for use := 1; use <= 5; use++ {
		if response := post("five-uses", `{"text":"hello"}`); response.Code != http.StatusNotFound || response.Body.String() != "channel_not_found" {
			t.Fatalf("valid use %d status=%d reason=%q", use, response.Code, response.Body)
		}
	}
	if response := post("five-uses", `{"text":"hello"}`); response.Code != http.StatusNotFound || response.Body.String() != "used_url" {
		t.Fatalf("spent URL status=%d reason=%q", response.Code, response.Body)
	}
	if response := post("never-issued", `{"text":"hello"}`); response.Code != http.StatusNotFound || response.Body.String() != "expired_url" {
		t.Fatalf("unknown URL status=%d reason=%q", response.Code, response.Body)
	}
}

func TestAppResponseFailureNeverAnswersAHandledErrorWithAServerError(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{service.ErrAppResponsePayloadInvalid, http.StatusBadRequest},
		{service.ErrAppResponseNoText, http.StatusBadRequest},
		{service.ErrAppResponseURLUsed, http.StatusNotFound},
		{service.ErrAppResponseURLExpired, http.StatusNotFound},
		{service.ErrInvalidAppResponse, http.StatusBadRequest},
		{service.ErrConversationAlreadyArchived, http.StatusGone},
		{service.ErrConversationPostingRestricted, http.StatusForbidden},
		{service.ErrNotInConversation, http.StatusNotFound},
		{store.ErrNotFound, http.StatusNotFound},
		{store.ErrTransient, http.StatusServiceUnavailable},
		{errors.New("unclassified"), http.StatusInternalServerError},
	} {
		if status, reason := appResponseFailure(test.err); status != test.status || reason == "" {
			t.Errorf("%v answered %d %q, want %d", test.err, status, reason, test.status)
		}
	}
}

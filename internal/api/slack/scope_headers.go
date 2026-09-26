package slack

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
)

// Slack answers every authenticated Web API call with two headers:
// X-OAuth-Scopes, the scopes the presented token holds, and
// X-Accepted-OAuth-Scopes, the scopes the method accepts. @slack/web-api
// surfaces them as response_metadata.scopes and acceptedScopes, and an app
// uses them to discover which grant it is missing without parsing an error.
// Neither was ever sent.
//
// Authentication happens deep inside each handler, long after the response
// writer could have been decorated, so the headers are carried by a per-request
// record: withOAuthScopeHeaders installs it in the request context, the
// authentication helpers fill it in, and the writer stamps it onto the
// response when the handler first writes. A request that never authenticated
// (api.test, an unknown method, a rate-limited call) carries neither header,
// as in Slack.

type oauthScopeRecordKey struct{}

type oauthScopeRecord struct {
	authenticated bool
	granted       []string
	accepted      []auth.Scope
}

func withOAuthScopeHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := &oauthScopeRecord{}
		next.ServeHTTP(&oauthScopeWriter{ResponseWriter: w, record: record}, r.WithContext(context.WithValue(r.Context(), oauthScopeRecordKey{}, record)))
	})
}

// recordGrantedScopes notes the authenticated principal's scopes. It is called
// by every authentication helper, including on a missing_scope refusal, where
// the header is most useful.
func recordGrantedScopes(r *http.Request, principal auth.Principal) {
	record, ok := r.Context().Value(oauthScopeRecordKey{}).(*oauthScopeRecord)
	if !ok {
		return
	}
	record.authenticated = true
	record.granted = permissionScopes(principal)
}

// recordAcceptedScopes notes the scopes the method accepts, any one of which
// satisfies it. A later, narrower check — the one scope a conversation's type
// needs — replaces an earlier family.
func recordAcceptedScopes(r *http.Request, scopes ...auth.Scope) {
	record, ok := r.Context().Value(oauthScopeRecordKey{}).(*oauthScopeRecord)
	if !ok {
		return
	}
	record.accepted = append(record.accepted[:0], scopes...)
}

type oauthScopeWriter struct {
	http.ResponseWriter
	record  *oauthScopeRecord
	stamped bool
}

func (w *oauthScopeWriter) stamp() {
	if w.stamped {
		return
	}
	w.stamped = true
	if !w.record.authenticated {
		return
	}
	granted := append([]string(nil), w.record.granted...)
	sort.Strings(granted)
	accepted := make([]string, 0, len(w.record.accepted))
	for _, scope := range w.record.accepted {
		if scope != "" {
			accepted = append(accepted, string(scope))
		}
	}
	w.Header().Set("X-OAuth-Scopes", strings.Join(granted, ","))
	w.Header().Set("X-Accepted-OAuth-Scopes", strings.Join(accepted, ","))
}

func (w *oauthScopeWriter) WriteHeader(status int) {
	w.stamp()
	w.ResponseWriter.WriteHeader(status)
}

func (w *oauthScopeWriter) Write(body []byte) (int, error) {
	w.stamp()
	return w.ResponseWriter.Write(body)
}

// Flush keeps streaming responses streaming through the wrapper.
func (w *oauthScopeWriter) Flush() {
	w.stamp()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *oauthScopeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

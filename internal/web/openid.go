package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/slackobject"
)

// Sign in with Slack's discovery surface. A relying party configured with this
// deployment's URL as its issuer finds every endpoint here, all at this
// deployment, in the same document Slack publishes at
// https://slack.com/.well-known/openid-configuration.

// validOpenIDResponseMode admits the response modes this server delivers: the
// redirect's query, the default for the code flow, and form_post.
func validOpenIDResponseMode(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "", "query", "form_post":
		return true
	}
	return false
}

// openIDResponseMode is the mode a Sign in with Slack request asked for. The
// app authorization flow always answers in the query.
func openIDResponseMode(fields map[string]string, openID bool) string {
	if !openID {
		return ""
	}
	return strings.TrimSpace(fields["response_mode"])
}

func (h Handler) openIDConfiguration(w http.ResponseWriter, _ *http.Request) {
	issuer := slackobject.Origin(h.PublicURL)
	if issuer == "" {
		// An issuer is the URL relying parties compare every ID token's iss
		// against. Without -auth-public-url there is none to publish, and
		// guessing one from the request would let a Host header choose it.
		writeOpenIDError(w, http.StatusNotFound, "issuer_not_configured")
		return
	}
	writeOpenIDJSON(w, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/openid/connect/authorize",
		"token_endpoint":                        issuer + "/api/openid.connect.token",
		"userinfo_endpoint":                     issuer + "/api/openid.connect.userInfo",
		"jwks_uri":                              issuer + "/openid/connect/keys",
		"scopes_supported":                      []string{"openid", "profile", "email"},
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"form_post", "query"},
		"grant_types_supported":                 []string{"authorization_code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"claims_supported":                      []string{"sub", "auth_time", "iss"},
		"claims_parameter_supported":            false,
		"request_parameter_supported":           false,
		"request_uri_parameter_supported":       false,
		"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic"},
	})
}

func (h Handler) openIDKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Messages.OpenIDKeys(r.Context())
	if errors.Is(err, domain.ErrAppCredentialKeyUnavailable) {
		writeOpenIDError(w, http.StatusServiceUnavailable, "signing_key_unavailable")
		return
	}
	if err != nil {
		writeOpenIDError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	values := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": key.KeyID, "n": key.Modulus, "e": key.Exponent})
	}
	writeOpenIDJSON(w, map[string]any{"keys": values})
}

func writeOpenIDJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func writeOpenIDError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// openIDFormPostScript submits the response form as soon as the page loads.
// The policy admits it by hash and nothing else, and the button beside it
// submits the same form where scripts do not run.
const openIDFormPostScript = `document.forms[0].submit();`

var openIDFormPostScriptHash = func() string {
	digest := sha256.Sum256([]byte(openIDFormPostScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
}()

var openIDFormPostTemplate = template.Must(template.New("form_post").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Signing you in</title></head>
<body><form method="post" action="{{.Action}}">{{range $name, $value := .Fields}}<input type="hidden" name="{{$name}}" value="{{$value}}">{{end}}<p>Returning you to the app.</p><button type="submit">Continue</button></form><script>` + openIDFormPostScript + `</script></body></html>`))

// postOAuthAuthorization delivers an authorization response by form_post: a
// page that posts the code or error, and the state, to the redirect URI. Its
// policy lets the form reach that redirect's origin and no other.
func postOAuthAuthorization(w http.ResponseWriter, _ *http.Request, rawRedirect, state, code, failure string) {
	target, err := url.Parse(rawRedirect)
	if err != nil || target.Scheme == "" || target.Host == "" {
		http.Error(w, "authorization redirect is invalid", http.StatusBadRequest)
		return
	}
	fields := map[string]string{}
	if code != "" {
		fields["code"] = code
	}
	if failure != "" {
		fields["error"] = failure
	}
	if state != "" {
		fields["state"] = state
	}
	origin := target.Scheme + "://" + target.Host
	secureHeaders(w, "default-src 'none'; script-src "+openIDFormPostScriptHash+"; form-action "+origin+"; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = openIDFormPostTemplate.Execute(w, map[string]any{"Action": target.String(), "Fields": fields})
}

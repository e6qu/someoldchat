// Package bearer reads the credential an HTTP Authorization header carries
// under the Bearer scheme. It is the one parser every surface shares — the
// Web API, its rate limiter, the monitoring endpoint and the activator's
// control plane — so they cannot disagree about which requests carry a token.
package bearer

import "strings"

// Token returns the credential in an Authorization header value of the form
// `Bearer <token>`. The auth-scheme is case-insensitive (RFC 9110 §11.1), so
// `bearer` and `BEARER` are the same scheme; each surface used to compare the
// literal prefix "Bearer ", and a client that lowercased it was told it had
// sent no credential. Any whitespace may separate the scheme from the token. A
// header naming another scheme, or carrying no token, reports false.
func Token(header string) (string, bool) {
	header = strings.TrimSpace(header)
	index := strings.IndexAny(header, " \t")
	if index < 0 || !strings.EqualFold(header[:index], "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(header[index+1:])
	return token, token != ""
}

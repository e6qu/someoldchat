package slack

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// This file is the one place the Slack transport decides which absolute origin
// the URLs it emits are built on: file downloads (url_private,
// url_private_download, permalink, permalink_public), the files.* v2 upload
// URL, the OAuth authorize URL a manifest create returns, the incoming webhook
// url and configuration_url an install returns, message permalinks
// (chat.getPermalink, pins, reactions, search) and auth.test's url. Official SDKs
// fetch those URLs verbatim — @slack/web-api's files.uploadV2 POSTs to
// upload_url, and every client downloads url_private with its bearer token —
// so a relative path, or an origin a request header could forge into an
// arbitrary scheme, breaks them.
//
// The configured public URL always wins: behind a proxy it is the only
// trustworthy statement of where clients reach this server. Without one the
// origin is the request's own: TLS on the connection, or an X-Forwarded-Proto
// naming exactly http or https (anything else is ignored rather than echoed),
// and the Host header.

// SetPublicURL configures the absolute origin every emitted URL is built on.
// It must be an absolute http or https URL with a host and no query, fragment
// or credentials; a trailing slash is dropped so paths join cleanly.
func (h *Handler) SetPublicURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		h.PublicURL = ""
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Slack API public URL must be an absolute http or https URL without query, fragment or credentials")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	h.PublicURL = parsed.String()
	return nil
}

// origin is the absolute base for a URL this transport emits in answer to r.
func (h Handler) origin(r *http.Request) string {
	if h.PublicURL != "" {
		return strings.TrimRight(h.PublicURL, "/")
	}
	return requestOrigin(r)
}

// originURL resolves a server-relative path — a permalink or incoming webhook
// path the service minted — against origin. A value that is already absolute
// passes through unchanged: a chat process from before paths were minted may
// still answer during a rolling deploy.
func originURL(origin, path string) string {
	if path == "" || strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(origin, "/") + path
}

// requestOrigin derives scheme://host from the request alone. It is the
// fallback for a deployment that configured no public URL.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

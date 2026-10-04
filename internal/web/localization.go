package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/sameoldchat/sameoldchat/internal/l10n"
)

// localeCookie carries the language a member chose in Language & region. The
// choice is also a member preference, so it follows them to another browser;
// the cookie is what lets the very first byte of a page be in that language,
// before any script has run.
const localeCookie = "sameoldchat_locale"

// languagePreference is the member preference Language & region's picker
// writes.
const languagePreference = "language"

// routes is what a set of routes is registered on: a ServeMux, or the
// localizing wrapper Register uses.
type routes interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
	Handle(pattern string, handler http.Handler)
}

// localizingMux registers every web route behind withLocale, so each request
// is answered in its reader's language without each handler resolving it.
type localizingMux struct{ *http.ServeMux }

func (m localizingMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.Handle(pattern, withLocale(http.HandlerFunc(handler)))
}

func (m localizingMux) Handle(pattern string, handler http.Handler) {
	m.ServeMux.Handle(pattern, withLocale(handler))
}

// withLocale resolves the request's locale — the member's choice, else the
// browser's Accept-Language, else the source locale — and carries it on the
// response writer, which is what every page and error writer already has in
// hand. Responses vary by the header that chose it.
func withLocale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chosen := l10n.Locale("")
		if cookie, err := r.Cookie(localeCookie); err == nil {
			chosen = l10n.Locale(strings.TrimSpace(cookie.Value))
		}
		locale := l10n.Negotiate(chosen, r.Header.Get("Accept-Language"))
		w.Header().Set("Content-Language", string(locale))
		w.Header().Add("Vary", "Accept-Language")
		next.ServeHTTP(&localeWriter{ResponseWriter: w, locale: locale}, r.WithContext(context.WithValue(r.Context(), localeKey{}, locale)))
	})
}

type localeKey struct{}

// requestLocale is the locale withLocale resolved for the request, for code
// that has the request rather than its writer.
func requestLocale(r *http.Request) l10n.Locale {
	if locale, ok := r.Context().Value(localeKey{}).(l10n.Locale); ok {
		return locale
	}
	return l10n.Default
}

// localeWriter is a response writer that knows its reader's locale. It passes
// streaming and connection takeover through, so a route behind it can still
// flush or hijack.
type localeWriter struct {
	http.ResponseWriter
	locale l10n.Locale
}

func (w *localeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *localeWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *localeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("web: the response writer cannot be hijacked")
}

// localeOf is the locale a response is written in: the one withLocale
// resolved, found through any writer wrapped around it, else the source.
func localeOf(w http.ResponseWriter) l10n.Locale {
	for w != nil {
		if localized, ok := w.(*localeWriter); ok {
			return localized.locale
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		w = unwrapper.Unwrap()
	}
	return l10n.Default
}

// localeFunctions are the template functions bound to one locale: t for a
// message, tn for a count message.
func localeFunctions(locale l10n.Locale) template.FuncMap {
	localizer := l10n.For(locale)
	return template.FuncMap{
		"t":  func(key string, args ...any) string { return localizer.T(key, args...) },
		"tn": func(key string, count int, args ...any) string { return localizer.N(key, count, args...) },
	}
}

// localizedPages is each page template's clone per locale. html/template binds
// functions when a template is parsed and refuses to clone one that has run,
// so a clone per locale is made once, at startup, with t and tn bound to that
// locale, and a request picks its clone rather than rebinding anything.
var (
	localizable    []*template.Template
	localizedPages map[*template.Template]map[l10n.Locale]*template.Template
	localizeOnce   sync.Once
)

// localizable registers a template whose text is localized. mustPage
// registers every page; a template parsed outside it registers itself.
func registerLocalizable(page *template.Template) *template.Template {
	localizable = append(localizable, page)
	return page
}

// The clones are made before anything renders: html/template refuses to
// clone a template that has executed.
func init() {
	localizeOnce.Do(buildLocalizedPages)
}

func buildLocalizedPages() {
	localizedPages = make(map[*template.Template]map[l10n.Locale]*template.Template, len(localizable))
	for _, page := range localizable {
		clones := map[l10n.Locale]*template.Template{}
		for _, catalog := range l10n.Supported() {
			if catalog.Locale == l10n.Default {
				continue
			}
			clones[catalog.Locale] = template.Must(page.Clone()).Funcs(localeFunctions(catalog.Locale))
		}
		localizedPages[page] = clones
	}
}

// localized is page as it renders in locale: the page itself for the source
// locale, its clone for any other.
func localized(page *template.Template, locale l10n.Locale) *template.Template {
	localizeOnce.Do(buildLocalizedPages)
	if clone, ok := localizedPages[page][locale]; ok {
		return clone
	}
	return page
}

// clientCatalog is the "client." messages in locale, encoded for the page's
// data-l10n attribute, where scripts read them through sameoldchatT.
func clientCatalog(locale l10n.Locale) string {
	encoded, err := json.Marshal(l10n.For(locale).Client())
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// languageOption is one entry of Language & region's picker.
type languageOption struct {
	Locale string
	Name   string
}

// languageOptions is every language the picker offers: the listed catalogs.
func languageOptions() []languageOption {
	var options []languageOption
	for _, catalog := range l10n.Supported() {
		if catalog.Listed {
			options = append(options, languageOption{Locale: string(catalog.Locale), Name: catalog.Name})
		}
	}
	return options
}

# Localization

Every string a person reads in the web client is meant to come from a message
catalog, so the product can be offered in another language by adding a
catalog rather than by editing markup. Only English is shipped today. The
system exists so a translation can be added later without touching the code
that renders it; moving the remaining English text onto the catalog happens
surface by surface (see [Migrating a surface](#migrating-a-surface)).

## How a request picks its language

`withLocale` (internal/web/localization.go) wraps every web route. It chooses
the locale in this order:

1. the language the member chose in Preferences › Language & region, carried
   by the `sameoldchat_locale` cookie so the first byte of a page is already in
   it (the choice is also a member preference, so it follows the member to
   another browser, where the shell sets the cookie on first load);
2. the browser's `Accept-Language`, best quality first, matching an exact tag
   before a base language (`de-AT` is answered by `de`);
3. English.

The response carries `Content-Language` and the page `<html lang>`, both
naming the locale chosen, and varies on `Accept-Language`.

## Catalogs

Catalogs are JSON files embedded from `internal/l10n/locales/`, one per BCP 47
tag:

```json
{
  "locale": "en",
  "name": "English",
  "messages": {
    "prefs.tab.region": "Language & region",
    "inbox.unread": {"one": "One unread message", "other": "{count} unread messages"}
  }
}
```

- **Keys** are dotted lowercase, grouped by the surface that shows them.
  Keys under `client.` are also sent to the browser for scripts.
- **Placeholders** are named, `{like_this}`, so a translation may reorder
  them. A count message always receives `{count}`.
- **Plurals** use the CLDR categories (`zero`, `one`, `two`, `few`, `many`,
  `other`); `other` is required, and a form the language never selects is
  refused.

`en.json` is the source catalog and is complete by definition. Any other
catalog may be partial: a key it lacks falls back to English for that key
alone. A catalog is refused at startup (the server exits with a configuration
error, and `-check-configuration` reports it) if it invents a key, changes
whether a key counts, or uses placeholders other than the source's.

## Using messages

In a template, `t` returns a message and `tn` a count message; arguments
follow as name and value pairs:

```gotemplate
<h3>{{t "prefs.region.title"}}</h3>
<p>{{tn "inbox.unread" .Unread}}</p>
<p>{{t "greeting" "name" .Name}}</p>
```

In Go, `l10n.For(locale).T(key, args...)` and `.N(key, count, args...)` do the
same; a handler finds its locale with `requestLocale(r)` or `localeOf(w)`.

In a script, `window.sameoldchatT(key, args, count)` reads the page's
`client.` messages and picks the plural form with the browser's own CLDR data
(`Intl.PluralRules`).

`TestEveryMessageKeyIsDefinedAndUsed` (internal/web) fails when markup or a
script names a key the source catalog lacks, or the catalog holds a key
nothing names.

## The pseudo-locale

`en-XA` is generated from the English catalog at startup: every message is
accented and bracketed, `Language & region` reading `[Ļåñĝûåĝé & ŕéĝîöñ]`.
Rendering a page in it — set the `sameoldchat_locale` cookie to `en-XA`, or
send `Accept-Language: en-XA` — shows at a glance which text comes from the
catalog and which is still written into the markup, that placeholders
survive, and where a layout clips a longer string. The language picker never
offers it.

## Adding a language

1. Add `internal/l10n/locales/<tag>.json` with the language's own name and
   whatever messages are translated; the rest fall back to English.
2. If the base language is new, add its CLDR cardinal plural rule to
   `pluralRules` in `internal/l10n/l10n.go`; a catalog without one is refused.
3. Run `go test ./internal/l10n ./internal/web`.

The language picker lists the new language on the next start.

## Migrating a surface

Replace each English string in a surface's markup or script with a key, add
the key and its English text to `en.json`, and render the surface in the
pseudo-locale to find anything missed. The Preferences dialog's section tabs
and its Language & region panel were migrated first, with the announcement a
changed preference makes; the rest of the web client is still English in its
markup and moves over the same way. Messages people write, and names they
give things, are never translated.

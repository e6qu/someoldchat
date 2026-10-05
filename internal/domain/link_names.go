package domain

import (
	"regexp"
	"strings"
)

// NameReferenceKind tells a name resolver which sigil introduced a plain-text
// name: "@" names a member or a user group, "#" a channel.
type NameReferenceKind byte

const (
	MemberNameReference  NameReferenceKind = '@'
	ChannelNameReference NameReferenceKind = '#'
)

var (
	// A plain-text reference starts the text or follows whitespace or an
	// opening parenthesis, so an e-mail address (a@b.example) or a URL
	// fragment (/page#top) is never read as one.
	memberNameReference  = regexp.MustCompile(`(^|[[:space:](])@([[:alnum:]_.-]+)`)
	channelNameReference = regexp.MustCompile(`(^|[[:space:](])#([[:alnum:]_-]+)`)
	// markupSpan is mrkdwn that already names its target: <@U…>, <#C…>,
	// <!subteam^…>, <https://…|label>. A name inside one is never relinked.
	markupSpan = regexp.MustCompile(`<[^<>\n]*>`)
)

// LinkNameReferences replaces the plain-text @name and #name references of
// mrkdwn text with the markup resolve returns for them, and leaves every
// reference resolve declines as the literal text it was. It is the single
// parser behind chat.postMessage's link_names and a slash command's
// should_escape: the callers differ only in the directory they resolve
// against and the markup they write.
//
// Names inside code (a fenced block or an inline span) and inside existing
// markup are not references. A name followed by sentence punctuation
// ("@alice." or "#general-") resolves without it, because member names may
// themselves contain "." and "-": the longest name that resolves wins.
func LinkNameReferences(text string, resolve func(kind NameReferenceKind, name string) (string, bool)) string {
	if resolve == nil || (!strings.Contains(text, "@") && !strings.Contains(text, "#")) {
		return text
	}
	// Members first, then channels: each pass reads the text the previous
	// one wrote, so the markup a member reference became is protected too.
	text = linkNameReferences(text, memberNameReference, MemberNameReference, resolve)
	return linkNameReferences(text, channelNameReference, ChannelNameReference, resolve)
}

func linkNameReferences(text string, pattern *regexp.Regexp, kind NameReferenceKind, resolve func(NameReferenceKind, string) (string, bool)) string {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var protected [][]int
	for _, code := range []*regexp.Regexp{fencedCode, inlineCode, markupSpan} {
		protected = append(protected, code.FindAllStringIndex(text, -1)...)
	}
	inside := func(at int) bool {
		for _, span := range protected {
			if at >= span[0] && at < span[1] {
				return true
			}
		}
		return false
	}
	var output strings.Builder
	last := 0
	for _, match := range matches {
		// match[4]:match[5] is the name; the sigil sits just before it.
		sigil, nameStart, nameEnd := match[4]-1, match[4], match[5]
		if inside(sigil) {
			continue
		}
		name := text[nameStart:nameEnd]
		for name != "" {
			if markup, ok := resolve(kind, name); ok {
				output.WriteString(text[last:sigil])
				output.WriteString(markup)
				last = nameStart + len(name)
				break
			}
			// Drop one trailing punctuation character at a time so
			// "@a.b." can still resolve "a.b".
			if !strings.ContainsRune(".-_", rune(name[len(name)-1])) {
				break
			}
			name = name[:len(name)-1]
		}
	}
	output.WriteString(text[last:])
	return output.String()
}

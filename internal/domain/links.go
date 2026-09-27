package domain

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// MaxUnfurlDomains is how many domains an app may register for link
// unfurling: the app manifest's features.unfurl_domains holds at most five.
const MaxUnfurlDomains = 5

// MaxLinkSharedLinks bounds the links one link_shared event names. Slack
// unfurls at most five links of a message; a message naming more reaches the
// app as the first five in the order they appear.
const MaxLinkSharedLinks = 5

var (
	// fencedCode and inlineCode are Slack's code formatting. Slack neither
	// links nor unfurls a URL written inside code, so those spans are removed
	// before the text is searched. An unclosed fence is literal text.
	fencedCode = regexp.MustCompile("(?s)```.*?```")
	inlineCode = regexp.MustCompile("`[^`\n]+`")
	// bareLink is a URL Slack links automatically when it is not already
	// wrapped in <angle brackets>.
	bareLink       = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)
	unescapeMrkdwn = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">")
)

// NormalizeUnfurlDomain lower-cases a registered unfurl domain and reports
// whether it is a bare host name: dot-separated labels of letters, digits and
// hyphens. Slack's app configuration refuses a scheme, a path, a port or a
// wildcard, because the domain is compared with the host of each shared link.
func NormalizeUnfurlDomain(value string) (string, bool) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if value == "" || len(value) > 253 {
		return "", false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return "", false
			}
		}
	}
	return value, true
}

// SharedLink is one entry of a link_shared event's links array.
type SharedLink struct {
	Domain string `json:"domain"`
	URL    string `json:"url"`
}

// MatchUnfurlDomain reports the registered domain a link belongs to. A domain
// matches its own host and every subdomain of it, as Slack matches a
// registered domain; when more than one registered domain matches, the most
// specific one is reported.
func MatchUnfurlDomain(domains []string, link string) (string, bool) {
	parsed, err := url.Parse(link)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return "", false
	}
	matched := ""
	for _, domain := range domains {
		domain, valid := NormalizeUnfurlDomain(domain)
		if !valid {
			continue
		}
		if (host == domain || strings.HasSuffix(host, "."+domain)) && len(domain) > len(matched) {
			matched = domain
		}
	}
	return matched, matched != ""
}

// SharedLinks selects the links an app's registered domains claim, in message
// order and at most MaxLinkSharedLinks of them.
func SharedLinks(domains []string, links []string) []SharedLink {
	shared := make([]SharedLink, 0, len(links))
	for _, link := range links {
		if len(shared) == MaxLinkSharedLinks {
			break
		}
		if domain, ok := MatchUnfurlDomain(domains, link); ok {
			shared = append(shared, SharedLink{Domain: domain, URL: link})
		}
	}
	return shared
}

// LinksInMessage lists the distinct http(s) links a message shares, in the
// order they first appear: the links of its mrkdwn text, <url> and <url|label>
// alike, followed by the link elements of its rich_text blocks. Links inside
// code - a fenced block, an inline span, a preformatted rich_text block or a
// code-styled link element - are not shared, since Slack does not unfurl them.
// Values are unescaped, so each is the URL exactly as chat.unfurl names it.
func LinksInMessage(text, blocks string) []string {
	var links []string
	seen := make(map[string]struct{})
	add := func(candidate string) {
		candidate = unescapeMrkdwn.Replace(strings.TrimSpace(candidate))
		parsed, err := url.Parse(candidate)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return
		}
		if _, duplicate := seen[candidate]; duplicate {
			return
		}
		seen[candidate] = struct{}{}
		links = append(links, candidate)
	}
	for _, link := range linksInMrkdwn(text) {
		add(link)
	}
	var value any
	if json.Unmarshal([]byte(blocks), &value) == nil {
		var visit func(any)
		visit = func(current any) {
			switch typed := current.(type) {
			case []any:
				for _, child := range typed {
					visit(child)
				}
			case map[string]any:
				switch typed["type"] {
				case "rich_text_preformatted":
					return
				case "link":
					style, _ := typed["style"].(map[string]any)
					if code, _ := style["code"].(bool); !code {
						if link, ok := typed["url"].(string); ok {
							add(link)
						}
					}
					return
				}
				if elements, ok := typed["elements"]; ok {
					visit(elements)
				}
			}
		}
		if list, ok := value.([]any); ok {
			for _, block := range list {
				if object, ok := block.(map[string]any); ok && object["type"] == "rich_text" {
					visit(object)
				}
			}
		}
	}
	return links
}

// linksInMrkdwn finds the links of mrkdwn text in the order they appear.
func linksInMrkdwn(text string) []string {
	text = fencedCode.ReplaceAllStringFunc(text, blank)
	text = inlineCode.ReplaceAllStringFunc(text, blank)
	type found struct {
		at   int
		link string
	}
	var candidates []found
	// <https://example.com> and <https://example.com|label>. Each is blanked
	// once read so the bare-URL scan below cannot find it a second time.
	var rest strings.Builder
	for index := 0; index < len(text); {
		open := strings.IndexByte(text[index:], '<')
		if open < 0 {
			rest.WriteString(text[index:])
			break
		}
		open += index
		closing := strings.IndexByte(text[open:], '>')
		if closing < 0 {
			rest.WriteString(text[index:])
			break
		}
		closing += open
		rest.WriteString(text[index:open])
		target, _, _ := strings.Cut(text[open+1:closing], "|")
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			candidates = append(candidates, found{at: open, link: target})
			rest.WriteString(blank(text[open : closing+1]))
		} else {
			rest.WriteString(text[open : closing+1])
		}
		index = closing + 1
	}
	remaining := rest.String()
	for _, span := range bareLink.FindAllStringIndex(remaining, -1) {
		link := strings.TrimRight(remaining[span[0]:span[1]], ".,;:!?)]}")
		candidates = append(candidates, found{at: span[0], link: link})
	}
	// Both scans preserve length, so an offset orders the two kinds together.
	slices.SortStableFunc(candidates, func(left, right found) int { return left.at - right.at })
	links := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		links = append(links, candidate.link)
	}
	return links
}

// blank replaces a span with spaces of the same byte length, so offsets into
// the text keep their meaning.
func blank(value string) string {
	return strings.Repeat(" ", len(value))
}

package domain

import (
	"reflect"
	"testing"
)

func TestLinksInMessageFindsSharedLinksAndSkipsCode(t *testing.T) {
	text := "see <https://docs.example.com/a|the docs>, <https://example.com/b?x=1&amp;y=2> and https://example.org/c. " +
		"Not `https://inline.example/x` or\n```\nhttps://fenced.example/y\n```\n<mailto:a@example.com|mail> <@U1> " +
		"again https://example.org/c"
	blocks := `[{"type":"rich_text","elements":[
		{"type":"rich_text_section","elements":[{"type":"link","url":"https://blocks.example/r"},{"type":"link","url":"https://code.example/s","style":{"code":true}}]},
		{"type":"rich_text_preformatted","elements":[{"type":"link","url":"https://pre.example/t"}]},
		{"type":"rich_text_list","elements":[{"type":"rich_text_section","elements":[{"type":"link","url":"https://list.example/u"}]}]}
	]},{"type":"section","text":{"type":"mrkdwn","text":"<https://section.example>"}}]`
	got := LinksInMessage(text, blocks)
	want := []string{
		"https://docs.example.com/a",
		"https://example.com/b?x=1&y=2",
		"https://example.org/c",
		"https://blocks.example/r",
		"https://list.example/u",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("links=%q\nwant %q", got, want)
	}
	if links := LinksInMessage("```unclosed https://example.com/open", ""); !reflect.DeepEqual(links, []string{"https://example.com/open"}) {
		t.Fatalf("an unclosed fence is literal text: %q", links)
	}
	if links := LinksInMessage("ftp://example.com <ftp://example.com> http:// nothing", ""); len(links) != 0 {
		t.Fatalf("non-http links were shared: %q", links)
	}
}

func TestUnfurlDomainsMatchTheirSubdomainsOnly(t *testing.T) {
	domains := []string{"example.com", "docs.example.com"}
	for link, want := range map[string]string{
		"https://example.com/x":          "example.com",
		"https://www.example.com/x":      "example.com",
		"https://docs.example.com/y":     "docs.example.com",
		"https://a.docs.example.com/y":   "docs.example.com",
		"http://EXAMPLE.com:8443/z":      "example.com",
		"https://notexample.com/":        "",
		"https://example.com.evil.test/": "",
		"mailto:someone@example.com":     "",
	} {
		got, ok := MatchUnfurlDomain(domains, link)
		if got != want || ok != (want != "") {
			t.Errorf("%s matched %q (%v), want %q", link, got, ok, want)
		}
	}
	for _, invalid := range []string{"https://example.com", "example.com/path", "example.com:443", "*.example.com", "exa mple.com", "-bad.example", ""} {
		if _, valid := NormalizeUnfurlDomain(invalid); valid {
			t.Errorf("%q was accepted as an unfurl domain", invalid)
		}
	}
	if normalized, valid := NormalizeUnfurlDomain(" Docs.Example.COM. "); !valid || normalized != "docs.example.com" {
		t.Fatalf("normalized=%q valid=%v", normalized, valid)
	}
}

func TestSharedLinksKeepTheClaimedLinksInOrderUpToTheCap(t *testing.T) {
	links := []string{"https://other.test/0"}
	for index := 1; index <= MaxLinkSharedLinks+2; index++ {
		links = append(links, "https://example.com/"+string(rune('a'+index)))
	}
	shared := SharedLinks([]string{"example.com"}, links)
	if len(shared) != MaxLinkSharedLinks || shared[0] != (SharedLink{Domain: "example.com", URL: "https://example.com/b"}) {
		t.Fatalf("shared=%+v", shared)
	}
	if shared := SharedLinks(nil, links); len(shared) != 0 {
		t.Fatalf("an app with no domains was handed links: %+v", shared)
	}
}

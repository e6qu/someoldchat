package web

import (
	"strings"
	"testing"
)

// The message body is displayed with white-space:pre-wrap, so a raw newline
// outside a <pre> is a second line break. Each case pins the block structure
// Slack gives the same source text.
func TestRenderSlackMrkdwnBlocks(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
	}{
		{"single line stays inline", "hello *there*", "hello <strong>there</strong>"},
		{"lines break once", "one\ntwo", "one<br>two"},
		{"blank line is one empty line", "one\n\ntwo", "one<br><br>two"},
		{"quote lines are one blockquote", "intro\n&gt;first\n&gt; second\nafter", "intro<blockquote>first<br>second</blockquote>after"},
		{"triple quote takes the rest", "lead\n&gt;&gt;&gt;quoted\nstill quoted", "lead<blockquote>quoted<br>still quoted</blockquote>"},
		{"fence is a block outside any paragraph", "before\n```\nfunc main() {\n  x := 1\n}\n```\nafter", "before<pre><code>func main() {\n  x := 1\n}</code></pre>after"},
		{"fence mid-line", "run ```go test``` now", "run <pre><code>go test</code></pre> now"},
		{"formatting inside a fence is literal", "```*not bold* <@U1>```", "<pre><code>*not bold* &lt;@U1&gt;</code></pre>"},
		{"unclosed fence is text", "a ``` b", "a ``` b"},
		{"bullet fallback is a list", "• one\n• two\ntail", "<ul><li>one</li><li>two</li></ul>tail"},
		{"numbered fallback is a list", "1. one\n2. two", "<ol><li>one</li><li>two</li></ol>"},
		{"a hyphen is not a list", "- one", "- one"},
		{"bold does not cross a line", "*a\nb*", "*a<br>b*"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := string(renderSlackMrkdwn(test.source))
			if got != test.want {
				t.Fatalf("renderSlackMrkdwn(%q)\n got %q\nwant %q", test.source, got, test.want)
			}
			if strings.Contains(strings.NewReplacer("<pre><code>", "\x00").Replace(got), "\n") && !strings.Contains(got, "<pre>") {
				t.Fatalf("rendered prose carries a raw newline: %q", got)
			}
		})
	}
}

func TestRenderSlackMentionsLinkAndFlagTheViewer(t *testing.T) {
	rendered := renderSlackMrkdwn("hi <@U123|@Ada Lovelace> and <@U999|@Grace>, see <#C42|general> <!here> <!date^1700000000^{date}|Nov 14>")
	got := string(rendered)
	for _, want := range []string{
		`<a class="slack-mention" href="/app/members?q=Ada+Lovelace" data-user-id="U123">@Ada Lovelace</a>`,
		`<a class="slack-mention" href="/app?channel=C42">#general</a>`,
		`<span class="slack-mention mention-broadcast">@here</span>`,
		`<span class="slack-date">Nov 14</span>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered %q\nmissing %q", got, want)
		}
	}
	marked := string(markSelfMentions(rendered, "U123"))
	if !strings.Contains(marked, `data-user-id="U123" data-self="true"`) || strings.Contains(marked, `data-user-id="U999" data-self`) {
		t.Fatalf("self mention flagging is wrong: %q", marked)
	}
	// A literal attribute typed by a member is escaped, so it cannot be flagged.
	typed := string(markSelfMentions(renderSlackMrkdwn(`data-user-id="U123"`), "U123"))
	if strings.Contains(typed, "data-self") {
		t.Fatalf("typed text was flagged as a mention: %q", typed)
	}
	// An identifier that is not ID-shaped never reaches an attribute.
	if got := string(renderSlackMrkdwn(`<@U1"x|@x>`)); strings.Contains(got, "href") {
		t.Fatalf("malformed user reference became a link: %q", got)
	}
}

func TestMentionsViewer(t *testing.T) {
	for _, test := range []struct {
		text string
		want bool
	}{
		{"hi <@U1>", true},
		{"hi <@U1|@Ada>", true},
		{"hi <@U12>", false},
		{"<!here> deploy", true},
		{"<!channel|@channel> heads up", true},
		{"no mention", false},
	} {
		if got := mentionsViewer(test.text, "U1"); got != test.want {
			t.Errorf("mentionsViewer(%q) = %v, want %v", test.text, got, test.want)
		}
	}
}

func TestJumbomoji(t *testing.T) {
	custom := map[string]string{"partyparrot": "https://example.test/p.gif"}
	for _, test := range []struct {
		text string
		want bool
	}{
		{":tada::rocket:", true},
		{" :tada: :partyparrot: ", true},
		{":wave::skin-tone-3:", true},
		{":tada: done", false},
		{":not_an_emoji_name:", false},
		{"", false},
		{strings.Repeat(":tada:", jumbomojiLimit), true},
		{strings.Repeat(":tada:", jumbomojiLimit+1), false},
	} {
		if got := jumbomoji(test.text, custom); got != test.want {
			t.Errorf("jumbomoji(%q) = %v, want %v", test.text, got, test.want)
		}
	}
}

func TestRenderSlackSkinToneModifiesTheGlyph(t *testing.T) {
	got := string(renderSlackMrkdwn(":wave::skin-tone-3:"))
	if strings.Contains(got, "skin-tone") || !strings.Contains(got, "\U0001F44B\U0001F3FC") {
		t.Fatalf("skin tone rendered as %q", got)
	}
}

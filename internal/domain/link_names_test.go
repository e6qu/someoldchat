package domain

import "testing"

func TestLinkNameReferences(t *testing.T) {
	names := map[NameReferenceKind]map[string]string{
		MemberNameReference:  {"bob": "<@U2>", "a.b": "<@U3>", "dev-team": "<!subteam^S1>"},
		ChannelNameReference: {"general": "<#C1>", "ops-alerts": "<#C2>"},
	}
	resolve := func(kind NameReferenceKind, name string) (string, bool) {
		markup, ok := names[kind][name]
		return markup, ok
	}
	for _, test := range []struct{ text, want string }{
		{"@bob", "<@U2>"},
		{"hi @bob, see #general.", "hi <@U2>, see <#C1>."},
		{"(@bob) and (#general)", "(<@U2>) and (<#C1>)"},
		// Trailing punctuation is not part of the name, but a name may
		// itself hold "." and "-": the longest name that resolves wins.
		{"ask @a.b. now", "ask <@U3>. now"},
		{"@dev-team- go", "<!subteam^S1>- go"},
		{"#ops-alerts-", "<#C2>-"},
		// Unresolved names, e-mail addresses and URL fragments stay literal.
		{"@nobody #nowhere bob@example.com https://x.test/a#general", "@nobody #nowhere bob@example.com https://x.test/a#general"},
		// Code and existing markup are not plain text.
		{"`@bob` ```\n#general\n``` <https://x.test|@bob> <@U9> <#C9|general>", "`@bob` ```\n#general\n``` <https://x.test|@bob> <@U9> <#C9|general>"},
		{"@bob\n#general", "<@U2>\n<#C1>"},
		{"", ""},
	} {
		if got := LinkNameReferences(test.text, resolve); got != test.want {
			t.Errorf("LinkNameReferences(%q) = %q, want %q", test.text, got, test.want)
		}
	}
	if got := LinkNameReferences("@bob", nil); got != "@bob" {
		t.Fatalf("nil resolver = %q", got)
	}
}

func TestMessageUnfurlsLinks(t *testing.T) {
	for _, test := range []struct {
		state string
		want  bool
	}{
		{"", true},
		{`{"bot_id":"B1"}`, true},
		{`{"bot_id":"B1","unfurl_links":true}`, true},
		{`{"bot_id":"B1","unfurl_links":false}`, false},
		{`{"unfurl_links":false}`, false},
		{`{"unfurl_media":false}`, true},
		{`{"bot_id":"B1","unfurl_media":false}`, true},
	} {
		if got := (Message{StreamState: test.state}).UnfurlsLinks(); got != test.want {
			t.Errorf("UnfurlsLinks(%s) = %v, want %v", test.state, got, test.want)
		}
	}
}

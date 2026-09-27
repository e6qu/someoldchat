package web

import (
	"strings"
	"testing"
)

func TestRenderCanvasMarkdownRendersBlocksAndInlineSafely(t *testing.T) {
	got := string(renderCanvasMarkdown("Steps for **the** _launch_ with `a*b` and [docs](https://example.test/a?b=1&c=2).\n\n- Freeze\n- Deploy\n\n1. First\n2. Second\n\n- [ ] Write notes\n- [x] QA\n\n> quoted\n\n```\n<b>raw</b>\n```\n[bad](javascript:alert(1)) <script>x</script>", nil))
	for _, want := range []string{
		"<p>Steps for <strong>the</strong> <em>launch</em> with <code>a*b</code> and <a href=\"https://example.test/a?b=1&amp;c=2\" rel=\"noopener noreferrer\" target=\"_blank\">docs</a>.</p>",
		"<ul><li>Freeze</li><li>Deploy</li></ul>",
		"<ol><li>First</li><li>Second</li></ol>",
		`<ul class="checklist"><li><span class="check" aria-hidden="true">☐</span><span class="visually-hidden">Not done: </span>Write notes</li><li><span class="check done" aria-hidden="true">☑</span><span class="visually-hidden">Done: </span>QA</li></ul>`,
		"<blockquote>quoted</blockquote>",
		"<pre><code>&lt;b&gt;raw&lt;/b&gt;</code></pre>",
		"[bad](javascript:alert(1)) &lt;script&gt;x&lt;/script&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered markdown is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script") || strings.Contains(got, `href="javascript`) {
		t.Fatalf("unsafe markup survived: %s", got)
	}
}

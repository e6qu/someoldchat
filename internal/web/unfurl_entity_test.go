package web

import "testing"

// A Work Object unfurl, attached by chat.unfurl's metadata entities, shows the
// entity's own title, link and product rather than a bare URL.
func TestWorkObjectUnfurlShowsTheEntity(t *testing.T) {
	link := "https://tasks.example/task/7"
	unfurls := decodeMessageUnfurls(map[string]string{
		link: `{"entity_type":"slack#/entities/task","url":"https://tasks.example/t/7","app_unfurl_url":"` + link + `","external_ref":{"id":"7"},"entity_payload":{"attributes":{"title":{"text":"Ship it"},"product_name":"Tasks"}}}`,
	})
	if len(unfurls) != 1 {
		t.Fatalf("unfurls = %+v", unfurls)
	}
	if got := unfurls[0]; got.Title != "Ship it" || got.TitleURL != "https://tasks.example/t/7" || got.Footer != "Tasks" {
		t.Fatalf("work object unfurl = %+v", got)
	}
}

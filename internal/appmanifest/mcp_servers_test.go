package appmanifest

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseReadsDeclaredMCPServers(t *testing.T) {
	parsed, problems := Parse(`{"display_information":{"name":"Agent tools"},"features":{"mcp_servers":[
		{"name":"search","url":"https://mcp.example.test/search"},
		{"name":" tickets ","url":"https://mcp.example.test/tickets"}
	]}}`)
	if len(problems) != 0 {
		t.Fatalf("problems=%+v", problems)
	}
	want := []MCPServer{{Name: "search", URL: "https://mcp.example.test/search"}, {Name: "tickets", URL: "https://mcp.example.test/tickets"}}
	if !reflect.DeepEqual(parsed.MCPServers, want) {
		t.Fatalf("servers=%+v, want %+v", parsed.MCPServers, want)
	}
	if parsed, problems := Parse(`{"display_information":{"name":"No servers"}}`); len(problems) != 0 || len(parsed.MCPServers) != 0 {
		t.Fatalf("an app without servers declared %+v (%+v)", parsed.MCPServers, problems)
	}
}

func TestParseRefusesAnUnusableMCPServerDeclaration(t *testing.T) {
	_, problems := Parse(`{"display_information":{"name":"Broken"},"features":{"mcp_servers":[
		"search",
		{"url":"https://mcp.example.test/a"},
		{"name":"a","url":"http://mcp.example.test/a"},
		{"name":"a","url":"https://mcp.example.test/b"},
		{"name":"b"}
	]}}`)
	pointers := make([]string, 0, len(problems))
	for _, problem := range problems {
		pointers = append(pointers, problem.Pointer)
	}
	sort.Strings(pointers)
	want := []string{
		"/features/mcp_servers/0",
		"/features/mcp_servers/1/name",
		"/features/mcp_servers/2/url",
		"/features/mcp_servers/3/name",
		"/features/mcp_servers/4/url",
	}
	if !reflect.DeepEqual(pointers, want) {
		t.Fatalf("pointers=%v, want %v", pointers, want)
	}
	if _, problems := Parse(`{"display_information":{"name":"Broken"},"features":{"mcp_servers":{"name":"a"}}}`); len(problems) != 1 || problems[0].Pointer != "/features/mcp_servers" {
		t.Fatalf("an object in place of the list: %+v", problems)
	}
}

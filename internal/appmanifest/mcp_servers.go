package appmanifest

import "fmt"

// MCPServer is one Model Context Protocol server an app declares.
//
// Slack's admin.apps.mcp.servers.* methods describe MCP servers as something
// an app has, identified per app, but the method references publish no
// manifest schema for declaring one. This is the minimal declaration those
// methods need and nothing more: a name that is unique within the app, which
// the server's identifier is derived from, and the HTTPS endpoint the server
// is reached at. It lives under features.mcp_servers, beside the app's other
// declared surfaces.
type MCPServer struct {
	Name string
	URL  string
}

// maxMCPServerNameLength bounds the name the identifier is derived from, so a
// manifest cannot make an administrative listing carry an unbounded string.
const maxMCPServerNameLength = 255

func parseMCPServers(features map[string]any, problems *[]Error) []MCPServer {
	const pointer = "/features/mcp_servers"
	raw, exists := features["mcp_servers"]
	if !exists {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		*problems = append(*problems, Error{Message: "MCP servers must be an array", Pointer: pointer})
		return nil
	}
	servers := make([]MCPServer, 0, len(values))
	names := make(map[string]bool, len(values))
	for index, value := range values {
		entry := fmt.Sprintf("%s/%d", pointer, index)
		server, ok := value.(map[string]any)
		if !ok {
			*problems = append(*problems, Error{Message: "MCP server must be an object", Pointer: entry})
			continue
		}
		name := stringField(server, "name")
		switch {
		case name == "":
			*problems = append(*problems, Error{Message: "MCP server name is required", Pointer: entry + "/name"})
		case len([]rune(name)) > maxMCPServerNameLength:
			*problems = append(*problems, Error{Message: fmt.Sprintf("MCP server name must be %d characters or fewer", maxMCPServerNameLength), Pointer: entry + "/name"})
		case names[name]:
			*problems = append(*problems, Error{Message: "MCP server name must be unique", Pointer: entry + "/name"})
		}
		names[name] = true
		endpoint := stringField(server, "url")
		if endpoint == "" {
			*problems = append(*problems, Error{Message: "MCP server url is required", Pointer: entry + "/url"})
		} else {
			validateHTTPSURL(endpoint, entry+"/url", problems)
		}
		servers = append(servers, MCPServer{Name: name, URL: endpoint})
	}
	return servers
}

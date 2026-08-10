package slack

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// FuzzNoRouteAnswersFiveHundred drives every registered Slack method with
// arbitrary bodies and content types and requires none of them to answer with a
// server error.
//
// The repository's rule is that a handled error must not become an HTTP 500:
// 500 is for an unhandled exception. Every route already has tests for the
// inputs somebody thought of, and this asks about the ones nobody did — a body
// that is not JSON where JSON is expected, a JSON scalar where an object is
// expected, a form where neither is, an empty body, a body that is only
// whitespace. Each of those is a client mistake, and a client mistake that
// reaches the panic handler is a defect in the route rather than in the client.
//
// The route list is read out of handler.go rather than written here, so a method
// registered tomorrow is fuzzed tomorrow without anybody remembering to add it.
func FuzzNoRouteAnswersFiveHundred(f *testing.F) {
	routes := registeredAPIRoutes(f)
	if len(routes) == 0 {
		f.Fatal("no routes were found in handler.go, which means the scan is broken rather than that there are none")
	}
	contentTypes := []string{
		"application/json",
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x",
		"text/plain",
		"",
	}
	f.Add(0, 0, "")
	f.Add(1, 0, "{}")
	f.Add(2, 0, "null")
	f.Add(3, 0, "[]")
	f.Add(4, 1, "channel=C1&text=hello")
	f.Add(5, 0, `{"channel":0}`)
	f.Add(6, 3, "   ")
	f.Fuzz(func(t *testing.T, routeIndex, contentIndex int, body string) {
		route := routes[abs(routeIndex)%len(routes)]
		contentType := contentTypes[abs(contentIndex)%len(contentTypes)]
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer token")
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		response := httptest.NewRecorder()
		testHandler().ServeHTTP(response, request)
		if response.Code == http.StatusInternalServerError {
			t.Fatalf("%s %s answered 500 for content-type %q and body %q; 500 is for an unhandled exception, and a client sending a bad body is not one",
				route.method, route.path, contentType, body)
		}
		if response.Code < http.StatusInternalServerError {
			return
		}
		// A 5xx that is not 500 may be entirely deliberate — apps.connections.open
		// answers 503 socket_mode_unavailable where the deployment offers no
		// Socket Mode, which is an honest answer rather than a fault. What
		// separates that from a fault is whether the client was told: a
		// deliberate refusal carries Slack's own shape, and a panic carries
		// whatever the recovery handler happened to write.
		var payload struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.OK || strings.TrimSpace(payload.Error) == "" {
			t.Fatalf("%s %s answered %d for content-type %q and body %q without a Slack-shaped refusal (body %q); a server error a client cannot read is indistinguishable from a crash",
				route.method, route.path, response.Code, contentType, body, response.Body.String())
		}
	})
}

func abs(value int) int {
	if value < 0 {
		if value == -1<<63 {
			return 0
		}
		return -value
	}
	return value
}

type apiRoute struct {
	method string
	path   string
}

// registeredAPIRoutes reads the `mux.HandleFunc("METHOD /api/...")` literals out
// of handler.go. The same literals the structural gates in this package parse,
// and for the same reason: a list written twice is a list that disagrees with
// itself.
func registeredAPIRoutes(f *testing.F) []apiRoute {
	f.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "handler.go", nil, 0)
	if err != nil {
		f.Fatalf("parse handler.go: %v", err)
	}
	seen := map[string]bool{}
	var routes []apiRoute
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "HandleFunc" {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		method, path, found := strings.Cut(pattern, " ")
		if !found || !strings.HasPrefix(path, "/api/") {
			return true
		}
		// A wildcard route needs a value the fuzzer cannot invent, and the
		// routes that carry one are covered by their own tests.
		if strings.Contains(path, "{") {
			return true
		}
		if seen[pattern] {
			return true
		}
		seen[pattern] = true
		routes = append(routes, apiRoute{method: method, path: path})
		return true
	})
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].path != routes[j].path {
			return routes[i].path < routes[j].path
		}
		return routes[i].method < routes[j].method
	})
	return routes
}

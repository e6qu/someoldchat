package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/realtime"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

var eventHeadAttribute = regexp.MustCompile(`<body data-event-head="([0-9]+)">`)

func renderedEventHead(t *testing.T, name string, response *httptest.ResponseRecorder) string {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", name, response.Code, response.Body)
	}
	match := eventHeadAttribute.FindStringSubmatch(response.Body.String())
	if match == nil {
		t.Fatalf("%s carries no render-time event head", name)
	}
	if !strings.Contains(response.Body.String(), liveStreamOpen) {
		t.Fatalf("%s does not open its stream from the rendered head", name)
	}
	return match[1]
}

// Every page that opens /events carries the journal head it was rendered at,
// and the stream opened from it delivers an event committed after the render
// but before the browser connected. A stream opened without a cursor starts at
// the head as of the connection and never carries that event, which is the
// window the rendered head closes.
func TestLivePagesOpenTheirStreamAtTheRenderedHead(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	head, err := messages.LatestEventSequence(context.Background(), "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	want := strconv.FormatUint(head, 10)
	canvas, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Live", `{"type":"markdown","markdown":"Now"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	head, err = messages.LatestEventSequence(context.Background(), "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	want = strconv.FormatUint(head, 10)
	for _, target := range []string{"/app?channel=Cdev", "/app/later", "/app/activity", "/app/apps", "/app/canvases/" + string(canvas.ID)} {
		if got := renderedEventHead(t, target, get(t, mux, target)); got != want {
			t.Fatalf("%s rendered head %s, want %s", target, got, want)
		}
	}
	if search := get(t, mux, "/app/search?q=hello"); strings.Contains(search.Body.String(), "data-event-head") {
		t.Fatal("a page with no live stream carries an event head")
	}

	rendered := renderedEventHead(t, "conversation", get(t, mux, "/app?channel=Cdev"))
	if posted := postForm(t, mux, "/app/message?channel=Cdev", "text=committed+between+render+and+connect", false); posted.Code != http.StatusSeeOther {
		t.Fatalf("post status=%d body=%s", posted.Code, posted.Body)
	}

	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	streams, err := realtime.NewHandler(messages, authenticator, messages, messages, messages)
	if err != nil {
		t.Fatal(err)
	}
	streamMux := http.NewServeMux()
	streams.Register(streamMux)
	server := httptest.NewServer(streamMux)
	defer server.Close()

	read := func(query string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		addBrowserCookies(request)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("stream status=%d", response.StatusCode)
		}
		var body strings.Builder
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			body.WriteString(scanner.Text() + "\n")
			if strings.Contains(scanner.Text(), "committed between render and connect") {
				break
			}
		}
		return body.String()
	}
	if body := read("?last_event_id=" + rendered); !strings.Contains(body, "committed between render and connect") {
		t.Fatalf("a stream opened at the rendered head lost the event committed before it connected: %q", body)
	}
	if body := read(""); strings.Contains(body, "committed between render and connect") {
		t.Fatalf("a stream with no cursor replayed history: %q", body)
	}
}

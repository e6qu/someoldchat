package realtime

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func dialRTM(t *testing.T, handler Handler, header http.Header) *websocket.Conn {
	t.Helper()
	mux := http.NewServeMux()
	handler.RegisterRTM(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/rtm?session_id=session-1", header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func livenessHandler(t *testing.T) Handler {
	t.Helper()
	handler, err := NewRTMHandler(emptyEventSource{}, testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, &testRTMMessageService{}, &testTypingSource{})
	if err != nil {
		t.Fatal(err)
	}
	handler.RTMPingPeriod = 50 * time.Millisecond
	return handler
}

// The RTM socket had no ping and no read deadline, so a peer that vanished
// without a TCP FIN held its stream until the operating system gave up. A
// client that answers the server's pings stays connected.
func TestRTMPingsTheClientAndKeepsAResponsiveOneConnected(t *testing.T) {
	client := dialRTM(t, livenessHandler(t), http.Header{"Origin": []string{"https://proxy.example.com"}})
	var pings atomic.Int32
	client.SetPingHandler(func(data string) error {
		pings.Add(1)
		return client.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})
	frames := make(chan string, 16)
	go func() {
		for {
			_, payload, err := client.ReadMessage()
			if err != nil {
				close(frames)
				return
			}
			frames <- string(payload)
		}
	}()
	deadline := time.After(time.Second)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("a client that answers pings was disconnected")
			}
			if strings.Contains(frame, "goodbye") {
				t.Fatalf("a client that answers pings was told %s", frame)
			}
		case <-deadline:
			if pings.Load() < 3 {
				t.Fatalf("pings=%d in one second at a 50ms period", pings.Load())
			}
			return
		}
	}
}

// A client that answers nothing is disconnected once the read deadline
// lapses. (The goodbye frame precedes the close, but a client that resumes
// reading may first fail answering a queued ping on the closed socket.)
func TestRTMDisconnectsAClientThatStopsAnswering(t *testing.T) {
	client := dialRTM(t, livenessHandler(t), nil)
	// Not reading means the client's pong never goes out.
	time.Sleep(300 * time.Millisecond)
	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := client.ReadMessage(); err != nil {
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				t.Fatal("the unresponsive client was never disconnected")
			}
			return
		}
	}
}

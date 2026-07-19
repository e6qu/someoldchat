package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

type responseDoer func(*http.Request) (*http.Response, error)

func (f responseDoer) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestHTTPResponseDeliveryRequiresAbsoluteURLAndClient(t *testing.T) {
	if _, err := newHTTPResponseDelivery("/responses", responseDoer(func(*http.Request) (*http.Response, error) { return nil, nil })); err == nil {
		t.Fatal("relative response URL succeeded")
	}
	if _, err := newHTTPResponseDelivery("https://example.test/responses", nil); err == nil {
		t.Fatal("nil HTTP client succeeded")
	}
}

func TestHTTPResponseDeliverySendsDurableResponseMetadata(t *testing.T) {
	var received *http.Request
	delivery, err := newHTTPResponseDelivery("https://example.test/responses", responseDoer(func(request *http.Request) (*http.Response, error) {
		received = request
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{"ok":true}`}
	if err := delivery(context.Background(), response); err != nil {
		t.Fatal(err)
	}
	if received == nil || received.Method != http.MethodPost || received.Header.Get("Content-Type") != "application/json" || received.Header.Get("Idempotency-Key") != "A1:env-1" || received.Header.Get("X-SameOldChat-App-ID") != "A1" || received.Header.Get("X-SameOldChat-Envelope-ID") != "env-1" {
		t.Fatalf("request=%+v", received)
	}
	body, err := io.ReadAll(received.Body)
	if err != nil || string(body) != response.Payload {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestHTTPResponseDeliveryFailsForNonSuccessStatus(t *testing.T) {
	delivery, err := newHTTPResponseDelivery("https://example.test/responses", responseDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery(context.Background(), domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{}`}); err == nil {
		t.Fatal("non-success response was accepted")
	}
}

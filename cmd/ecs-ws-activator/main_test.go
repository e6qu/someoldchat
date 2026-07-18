package main

import "testing"

func TestRandomIDAcceptsBackgroundOwnerWithoutRequest(t *testing.T) {
	if randomID(nil) == "" {
		t.Fatal("randomID returned an empty owner ID")
	}
}

func TestEndpointURLsUseConfiguredPortAndIPv6Syntax(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "readiness IPv4", got: readinessURL(endpoint{address: "10.0.0.4"}, 8080), want: "http://10.0.0.4:8080/readyz"},
		{name: "websocket IPv4", got: websocketEndpointURL(endpoint{address: "10.0.0.4"}, 8080, "/socket"), want: "ws://10.0.0.4:8080/socket"},
		{name: "readiness IPv6", got: readinessURL(endpoint{address: "2001:db8::4"}, 8080), want: "http://[2001:db8::4]:8080/readyz"},
		{name: "websocket IPv6", got: websocketEndpointURL(endpoint{address: "2001:db8::4"}, 8080, "/socket"), want: "ws://[2001:db8::4]:8080/socket"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got != testCase.want {
				t.Fatalf("URL=%q, want %q", testCase.got, testCase.want)
			}
		})
	}
}

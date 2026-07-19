package main

import "testing"

func TestValidControlTokenRequiresBearerSchemeAndExactToken(t *testing.T) {
	for _, test := range []struct {
		header string
		want   bool
	}{
		{header: "Bearer secret", want: true},
		{header: "Bearer  secret ", want: true},
		{header: "secret", want: false},
		{header: "bearer secret", want: false},
		{header: "Bearer", want: false},
		{header: "Bearer other", want: false},
		{header: "Bearer secret extra", want: false},
	} {
		if got := validControlToken(test.header, "secret"); got != test.want {
			t.Fatalf("validControlToken(%q)=%t, want %t", test.header, got, test.want)
		}
	}
}

func TestValidControlTokenRejectsEmptyExpectedToken(t *testing.T) {
	if validControlToken("Bearer secret", "") {
		t.Fatal("empty expected control token was accepted")
	}
}

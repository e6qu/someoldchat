package main

import "testing"

func TestRandomIDAcceptsBackgroundOwnerWithoutRequest(t *testing.T) {
	if randomID(nil) == "" {
		t.Fatal("randomID returned an empty owner ID")
	}
}

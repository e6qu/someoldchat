package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestActiveMessageStreamPrefixMatchesTheEncoding holds the stores' stream
// lookup to the encoding it relies on: a stream in progress encodes with
// ActiveMessageStreamPrefix and a finished one does not.
func TestActiveMessageStreamPrefixMatchesTheEncoding(t *testing.T) {
	active, err := json.Marshal(MessageStreamState{Active: true, TaskDisplayMode: "plan", Username: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(active), ActiveMessageStreamPrefix) {
		t.Fatalf("an active stream encodes as %s, which does not begin %s", active, ActiveMessageStreamPrefix)
	}
	finished, err := json.Marshal(MessageStreamState{Active: false, TaskDisplayMode: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(finished), ActiveMessageStreamPrefix) {
		t.Fatalf("a finished stream encodes as %s, which reads as active", finished)
	}
}

// TestAgentSessionStatusIsTheMostDemandingAgents is the reference's
// derivation: suspended > processing > active > closed.
func TestAgentSessionStatusIsTheMostDemandingAgents(t *testing.T) {
	session := func(statuses ...AgentSessionStatus) AgentSession {
		value := AgentSession{}
		for _, status := range statuses {
			value.Agents = append(value.Agents, AgentSessionAgent{Status: status})
		}
		return value
	}
	for _, test := range []struct {
		statuses []AgentSessionStatus
		want     AgentSessionStatus
	}{
		{nil, AgentSessionClosed},
		{[]AgentSessionStatus{AgentSessionClosed, AgentSessionActive}, AgentSessionActive},
		{[]AgentSessionStatus{AgentSessionActive, AgentSessionProcessing}, AgentSessionProcessing},
		{[]AgentSessionStatus{AgentSessionProcessing, AgentSessionSuspended, AgentSessionActive}, AgentSessionSuspended},
		{[]AgentSessionStatus{AgentSessionClosed}, AgentSessionClosed},
	} {
		if got := session(test.statuses...).Status(); got != test.want {
			t.Errorf("%v: %s, want %s", test.statuses, got, test.want)
		}
	}
	for _, status := range []AgentSessionStatus{"", "thinking", "Active"} {
		if status.Valid() {
			t.Errorf("%q is accepted", status)
		}
	}
}

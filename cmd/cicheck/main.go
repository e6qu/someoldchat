package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var actionReference = regexp.MustCompile(`^[^/\s]+/[^@\s]+@[0-9a-fA-F]{40}$`)

func main() {
	path := ".github/workflows/ci.yml"
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "cicheck: usage: cicheck [workflow]")
		os.Exit(2)
	}
	if len(os.Args) == 2 {
		path = os.Args[1]
	}
	if err := checkWorkflow(path); err != nil {
		fmt.Fprintln(os.Stderr, "cicheck:", err)
		os.Exit(1)
	}
}

func checkWorkflow(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(body), "\n")
	inTrigger := false
	pullRequestTrigger := false
	usesCount := 0
	for lineNumber, line := range lines {
		trimmed := strings.TrimSpace(line)
		if line == "on:" {
			inTrigger = true
			continue
		}
		if inTrigger && len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			inTrigger = false
		}
		if inTrigger {
			triggerName := strings.TrimSuffix(trimmed, ":")
			switch triggerName {
			case "pull_request":
				pullRequestTrigger = true
			case "push", "workflow_dispatch", "schedule", "workflow_call", "pull_request_target":
				return fmt.Errorf("workflow has forbidden trigger %q on line %d", triggerName, lineNumber+1)
			}
		}
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && strings.HasSuffix(trimmed, ":") {
			key := strings.TrimSuffix(trimmed, ":")
			switch key {
			case "push", "workflow_dispatch", "schedule", "workflow_call", "pull_request_target":
				return fmt.Errorf("workflow has forbidden top-level trigger %q on line %d", key, lineNumber+1)
			}
		}
		usesLine := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		if strings.HasPrefix(usesLine, "uses:") {
			usesCount++
			value := strings.TrimSpace(strings.TrimPrefix(usesLine, "uses:"))
			if hash := strings.IndexByte(value, '#'); hash >= 0 {
				value = strings.TrimSpace(value[:hash])
			}
			if !actionReference.MatchString(value) {
				return fmt.Errorf("action reference %q on line %d is not pinned to a full commit SHA", value, lineNumber+1)
			}
		}
	}
	if !pullRequestTrigger {
		return fmt.Errorf("workflow must declare a pull_request trigger")
	}
	if usesCount == 0 {
		return fmt.Errorf("workflow contains no action references")
	}
	return nil
}

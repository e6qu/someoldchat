package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
)

type inventory struct {
	Version     int    `yaml:"version"`
	Project     string `yaml:"project"`
	RetrievedAt string `yaml:"retrieved_at"`
	Status      string `yaml:"status"`
	SDKs        []sdk  `yaml:"sdks"`
	Bolt        []sdk  `yaml:"bolt"`
}

type sdk struct {
	ID        string `yaml:"id"`
	Ecosystem string `yaml:"ecosystem"`
	Package   string `yaml:"package"`
	Upstream  string `yaml:"upstream"`
	Release   string `yaml:"release"`
	Revision  string `yaml:"revision"`
	Artifact  string `yaml:"artifact"`
	SHA256    string `yaml:"sha256"`
	License   string `yaml:"license"`
	Suite     string `yaml:"suite"`
	SuitePath string `yaml:"suite_path"`
	Note      string `yaml:"note"`
}

var validSuiteStatuses = map[string]struct{}{"pending": {}, "passed": {}}

func main() {
	requireQualified := flag.Bool("require-qualified", false, "require immutable artifacts and passing SDK suites")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(errors.New("sdkcheck does not accept positional arguments"))
	}
	body, err := os.ReadFile("specs/sdk-compatibility.yaml")
	if err != nil {
		fail(err)
	}
	var value inventory
	if err := yaml.UnmarshalStrict(body, &value); err != nil {
		fail(fmt.Errorf("decode SDK inventory: %w", err))
	}
	if value.Version != 1 || value.Project == "" || value.RetrievedAt == "" || value.Status == "" || len(value.SDKs) == 0 || len(value.Bolt) == 0 {
		fail(errors.New("SDK inventory requires version, status, and at least one SDK"))
	}
	seen := make(map[string]struct{}, len(value.SDKs))
	for _, item := range value.SDKs {
		if item.ID == "" || item.Package == "" || item.Release == "" || item.Revision == "" || item.SHA256 == "" || item.Suite == "" {
			fail(fmt.Errorf("SDK %q is incomplete", item.ID))
		}
		if _, ok := validSuiteStatuses[item.Suite]; !ok {
			fail(fmt.Errorf("SDK %q has invalid suite status %q", item.ID, item.Suite))
		}
		if item.Suite == "passed" && item.SuitePath == "" {
			fail(fmt.Errorf("SDK %q reports a passed suite without suite_path", item.ID))
		}
		if _, exists := seen[item.ID]; exists {
			fail(fmt.Errorf("duplicate SDK ID %q", item.ID))
		}
		seen[item.ID] = struct{}{}
		if *requireQualified && (item.Revision == "pending-immutable-release-resolution" || item.SHA256 == "pending-artifact-resolution" || item.Suite != "passed") {
			fail(fmt.Errorf("SDK %q is not qualified", item.ID))
		}
	}
	for _, item := range value.Bolt {
		if item.ID == "" || item.Package == "" || item.Release == "" || item.Suite == "" {
			fail(fmt.Errorf("Bolt SDK %q is incomplete", item.ID))
		}
		if _, ok := validSuiteStatuses[item.Suite]; !ok {
			fail(fmt.Errorf("Bolt SDK %q has invalid suite status %q", item.ID, item.Suite))
		}
		if item.Suite == "passed" && (item.Revision == "" || item.Artifact == "" || item.SHA256 == "" || item.SuitePath == "") {
			fail(fmt.Errorf("Bolt SDK %q reports a passed suite without immutable qualification fields", item.ID))
		}
		if *requireQualified && (item.Revision == "" || item.Artifact == "" || item.SHA256 == "" || item.Suite != "passed") {
			fail(fmt.Errorf("Bolt SDK %q is not qualified", item.ID))
		}
	}
	if *requireQualified && strings.TrimSpace(value.Status) != "qualified" {
		fail(fmt.Errorf("SDK inventory status is %q, want qualified", value.Status))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sdkcheck:", err)
	os.Exit(1)
}

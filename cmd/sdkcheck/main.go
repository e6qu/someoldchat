package main

import (
	"encoding/hex"
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
	if err := validateInventory(value, *requireQualified); err != nil {
		fail(err)
	}
}

func validateInventory(value inventory, requireQualified bool) error {
	if value.Version != 1 || value.Project == "" || value.RetrievedAt == "" || value.Status == "" || len(value.SDKs) == 0 || len(value.Bolt) == 0 {
		return errors.New("SDK inventory requires version, status, and at least one SDK")
	}
	seen := make(map[string]struct{}, len(value.SDKs))
	for _, item := range value.SDKs {
		if err := validateSDK(item, "SDK", requireQualified); err != nil {
			return err
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("duplicate SDK ID %q", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	for _, item := range value.Bolt {
		if err := validateSDK(item, "Bolt SDK", requireQualified); err != nil {
			return err
		}
	}
	if requireQualified && strings.TrimSpace(value.Status) != "qualified" {
		return fmt.Errorf("SDK inventory status is %q, want qualified", value.Status)
	}
	return nil
}

func validateSDK(item sdk, label string, requireQualified bool) error {
	if item.ID == "" || item.Ecosystem == "" || item.Package == "" || item.Upstream == "" || item.Release == "" || item.Revision == "" || item.Artifact == "" || item.SHA256 == "" || item.License == "" || item.Suite == "" {
		return fmt.Errorf("%s %q is incomplete", label, item.ID)
	}
	if _, ok := validSuiteStatuses[item.Suite]; !ok {
		return fmt.Errorf("%s %q has invalid suite status %q", label, item.ID, item.Suite)
	}
	if len(item.SHA256) != 64 {
		return fmt.Errorf("%s %q has an invalid SHA-256 digest", label, item.ID)
	}
	if _, err := hex.DecodeString(item.SHA256); err != nil {
		return fmt.Errorf("%s %q has an invalid SHA-256 digest: %w", label, item.ID, err)
	}
	if item.Suite == "passed" && item.SuitePath == "" {
		return fmt.Errorf("%s %q reports a passed suite without suite_path", label, item.ID)
	}
	if requireQualified && (item.Revision == "pending-immutable-release-resolution" || item.SHA256 == "pending-artifact-resolution" || item.Suite != "passed") {
		return fmt.Errorf("%s %q is not qualified", label, item.ID)
	}
	if item.Suite == "passed" {
		info, err := os.Stat(item.SuitePath)
		if err != nil {
			return fmt.Errorf("%s %q qualification suite %q is unavailable: %w", label, item.ID, item.SuitePath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s %q qualification suite %q is not a regular file", label, item.ID, item.SuitePath)
		}
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sdkcheck:", err)
	os.Exit(1)
}

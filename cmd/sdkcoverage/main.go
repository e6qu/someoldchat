// Command sdkcoverage compares methods actually requested by pinned official
// Slack SDKs with the compatibility evidence claimed for each Web API method.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v2"
)

type ledger struct {
	Operations []ledgerOperation `yaml:"operations"`
}

type ledgerOperation struct {
	Method string `yaml:"method"`
	Status string `yaml:"status"`
}

var evidenceRank = map[string]int{
	"unimplemented":          0,
	"schema-compatible":      1,
	"sdk-compatible":         2,
	"behavior-compatible":    3,
	"verified-against-slack": 4,
}

func main() {
	input := flag.String("input", "", "newline-delimited methods observed at the SDK fixture")
	requireClaimed := flag.Bool("require-claimed", false, "fail when a method claimed at SDK-compatible or above was not observed")
	flag.Parse()
	if flag.NArg() != 0 || strings.TrimSpace(*input) == "" {
		fail(errors.New("sdkcoverage requires -input and accepts no positional arguments"))
	}
	if err := compareCoverage(*input, "specs/compatibility.yaml", *requireClaimed); err != nil {
		fail(err)
	}
}

func compareCoverage(inputPath, ledgerPath string, requireClaimed bool) error {
	body, err := os.ReadFile(ledgerPath)
	if err != nil {
		return err
	}
	var compatibility ledger
	// contractcheck owns strict validation of the complete ledger schema. This
	// command intentionally projects only method and evidence status, so new
	// provenance or audit metadata must not require duplicate structs here.
	if err := yaml.Unmarshal(body, &compatibility); err != nil {
		return fmt.Errorf("decode compatibility ledger: %w", err)
	}
	known := make(map[string]ledgerOperation, len(compatibility.Operations))
	for _, operation := range compatibility.Operations {
		known[strings.ToLower(operation.Method)] = operation
	}

	source, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer source.Close()
	observed := make(map[string]struct{})
	scanner := bufio.NewScanner(source)
	for scanner.Scan() {
		method := strings.TrimSpace(scanner.Text())
		if method == "" {
			continue
		}
		operation, exists := known[strings.ToLower(method)]
		if !exists {
			return fmt.Errorf("official SDK requested untracked method %q", method)
		}
		observed[strings.ToLower(operation.Method)] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(observed) == 0 {
		return errors.New("official SDK coverage log is empty")
	}

	claimed := 0
	missing := make([]string, 0)
	for _, operation := range compatibility.Operations {
		if evidenceRank[operation.Status] < evidenceRank["sdk-compatible"] {
			continue
		}
		claimed++
		if _, exists := observed[strings.ToLower(operation.Method)]; !exists {
			missing = append(missing, operation.Method)
		}
	}
	sort.Strings(missing)
	fmt.Printf("sdk.methods-observed=%d sdk.claimed=%d sdk.claimed-observed=%d/%d\n", len(observed), claimed, claimed-len(missing), claimed)
	if len(missing) != 0 {
		fmt.Printf("sdk.claimed-gaps=%s\n", strings.Join(missing, ","))
		if requireClaimed {
			return fmt.Errorf("%d SDK-compatible-or-better methods were not requested by an official SDK", len(missing))
		}
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sdkcoverage:", err)
	os.Exit(1)
}

// Command journeycheck validates the source-backed UI journey catalog and the
// stable IDs cited by executable browser evidence.
package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	journeyHeading = regexp.MustCompile(`(?m)^## ([A-Z][A-Z0-9]*(?:-[A-Z0-9]+)+) — `)
	checkedDate    = regexp.MustCompile(`(?m)^Sources checked ([0-9]{4}-[0-9]{2}-[0-9]{2}):$`)
	sourceMapRow   = regexp.MustCompile(`(?m)^\| ([A-Z][A-Z0-9]*(?:-[A-Z0-9]+)+) \| \[[^]\n]+\]\((https://[^)\n]+)\) \| ([^|\n]+) \|$`)
	evidenceGroup  = regexp.MustCompile(`\[((?:[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)+(?:[ \t]+)?)+)\]`)
	evidenceID     = regexp.MustCompile(`[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)+`)
)

func main() {
	if err := verifyCatalog(
		"specs/journeys",
		"tests/browser/specs",
		"tests/external-contract-qualification/qualify.sh",
	); err != nil {
		fmt.Fprintln(os.Stderr, "journeycheck:", err)
		os.Exit(1)
	}
}

func verifyCatalog(catalogDirectory, browserDirectory string, externalEvidencePaths ...string) error {
	documents, err := filepath.Glob(filepath.Join(catalogDirectory, "[0-9][0-9]-*.md"))
	if err != nil {
		return err
	}
	if len(documents) == 0 {
		return errors.New("journey catalog contains no numbered Markdown files")
	}
	sort.Strings(documents)
	known := make(map[string]string)
	for _, path := range documents {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		headings := journeyHeading.FindAllStringSubmatch(text, -1)
		if len(headings) == 0 {
			return fmt.Errorf("%s defines no stable journey IDs", path)
		}
		if !checkedDate.MatchString(text) {
			return fmt.Errorf("%s does not record a Sources checked YYYY-MM-DD date", path)
		}
		documentIDs := make(map[string]struct{}, len(headings))
		for _, heading := range headings {
			id := heading[1]
			if previous, exists := known[id]; exists {
				return fmt.Errorf("journey ID %s is duplicated in %s and %s", id, previous, path)
			}
			known[id] = path
			documentIDs[id] = struct{}{}
		}
		mapped := make(map[string]struct{}, len(documentIDs))
		for _, row := range sourceMapRow.FindAllStringSubmatch(text, -1) {
			id, sourceURL, assertion := row[1], row[2], strings.TrimSpace(row[3])
			if _, exists := documentIDs[id]; !exists {
				return fmt.Errorf("%s source map cites unknown local journey ID %s", path, id)
			}
			if _, exists := mapped[id]; exists {
				return fmt.Errorf("%s source map duplicates journey ID %s", path, id)
			}
			if !isOfficialSlackURL(sourceURL) {
				return fmt.Errorf("%s source map for %s does not use an official Slack source", path, id)
			}
			if len(assertion) < 12 {
				return fmt.Errorf("%s source map for %s does not state the externally supported behavior", path, id)
			}
			mapped[id] = struct{}{}
		}
		for id := range documentIDs {
			if _, exists := mapped[id]; !exists {
				return fmt.Errorf("%s has no journey-source map row for %s", path, id)
			}
		}
	}

	specifications, err := filepath.Glob(filepath.Join(browserDirectory, "*.mjs"))
	if err != nil {
		return err
	}
	if len(specifications) == 0 {
		return errors.New("browser suite contains no .mjs specifications")
	}
	cited := make(map[string]struct{})
	for _, path := range specifications {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, group := range evidenceGroup.FindAllStringSubmatch(string(body), -1) {
			for _, id := range evidenceID.FindAllString(group[1], -1) {
				if _, exists := known[id]; !exists {
					return fmt.Errorf("%s cites unknown journey ID %s", path, id)
				}
				cited[id] = struct{}{}
			}
		}
	}

	externallyCited := make(map[string]struct{})
	for _, path := range externalEvidencePaths {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, group := range evidenceGroup.FindAllStringSubmatch(string(body), -1) {
			for _, id := range evidenceID.FindAllString(group[1], -1) {
				if _, exists := known[id]; !exists {
					return fmt.Errorf("%s cites unknown journey ID %s", path, id)
				}
				externallyCited[id] = struct{}{}
			}
		}
	}

	uncovered := make([]string, 0, len(known)-len(cited))
	for id := range known {
		if _, exists := cited[id]; !exists {
			uncovered = append(uncovered, id)
		}
	}
	sort.Strings(uncovered)
	fmt.Printf("journeys.catalog=%d journeys.source-mapped=%d/%d journeys.external-source-cited=%d/%d journeys.browser-cited=%d/%d\n", len(known), len(known), len(known), len(externallyCited), len(known), len(cited), len(known))
	if len(externalEvidencePaths) != 0 && len(externallyCited) != len(known) {
		externalGaps := make([]string, 0, len(known)-len(externallyCited))
		for id := range known {
			if _, exists := externallyCited[id]; !exists {
				externalGaps = append(externalGaps, id)
			}
		}
		sort.Strings(externalGaps)
		fmt.Printf("journeys.external-source-gaps=%s\n", strings.Join(externalGaps, ","))
	}
	if len(uncovered) != 0 {
		fmt.Printf("journeys.browser-gaps=%s\n", strings.Join(uncovered, ","))
	}
	return nil
}

func isOfficialSlackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "slack.com" || host == "www.slack.com" ||
		host == "api.slack.com" || host == "docs.slack.dev"
}

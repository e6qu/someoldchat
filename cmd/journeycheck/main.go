// Command journeycheck validates the source-backed UI journey catalog and the
// stable IDs cited by executable browser evidence.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	journeyHeading = regexp.MustCompile(`(?m)^## ([A-Z][A-Z0-9]*(?:-[A-Z0-9]+)+) — `)
	checkedDate    = regexp.MustCompile(`(?m)^Sources checked ([0-9]{4}-[0-9]{2}-[0-9]{2}):$`)
	officialSource = regexp.MustCompile(`https://(?:docs\.slack\.dev|api\.slack\.com|(?:[A-Za-z0-9-]+\.)?slack\.com)/`)
	evidenceGroup  = regexp.MustCompile(`\[((?:[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)+(?:[ \t]+)?)+)\]`)
	evidenceID     = regexp.MustCompile(`[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)+`)
)

func main() {
	if err := verifyCatalog("specs/journeys", "tests/browser/specs"); err != nil {
		fmt.Fprintln(os.Stderr, "journeycheck:", err)
		os.Exit(1)
	}
}

func verifyCatalog(catalogDirectory, browserDirectory string) error {
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
		if !officialSource.MatchString(text) {
			return fmt.Errorf("%s names no official Slack source", path)
		}
		for _, heading := range headings {
			id := heading[1]
			if previous, exists := known[id]; exists {
				return fmt.Errorf("journey ID %s is duplicated in %s and %s", id, previous, path)
			}
			known[id] = path
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

	uncovered := make([]string, 0, len(known)-len(cited))
	for id := range known {
		if _, exists := cited[id]; !exists {
			uncovered = append(uncovered, id)
		}
	}
	sort.Strings(uncovered)
	fmt.Printf("journeys.catalog=%d journeys.browser-cited=%d/%d\n", len(known), len(cited), len(known))
	if len(uncovered) != 0 {
		fmt.Printf("journeys.browser-gaps=%s\n", strings.Join(uncovered, ","))
	}
	return nil
}

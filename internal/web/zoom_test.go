package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The zoom preference scales the whole page with CSS zoom, and a viewport
// unit inside a zoomed page is scaled with it: 100vh becomes 150% of the
// window at 150% zoom. Every viewport length therefore divides by --zoom, so
// what is sized to the window still fits it; a new rule written with a bare
// unit would overflow the window for a member who zooms.
func TestViewportLengthsDivideByTheZoom(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	length := regexp.MustCompile(`(?:^|[^\w.-])\d+(?:\.\d+)?d?v[hw]\b( / var\(--zoom, 1\))?`)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range length.FindAllStringSubmatch(string(source), -1) {
			if match[1] == "" {
				t.Errorf("%s: %q is not divided by var(--zoom, 1)", file, strings.TrimSpace(match[0]))
			}
		}
	}
}

// Each zoom the Accessibility preference offers has a factor, and the early
// script that applies a stored zoom before the page paints accepts exactly
// those values.
func TestZoomChoicesHaveFactors(t *testing.T) {
	offered := regexp.MustCompile(`<option value="(\d+)">\d+%</option>`).FindAllStringSubmatch(shellPartials, -1)
	if len(offered) == 0 {
		t.Fatal("the Accessibility panel offers no zoom")
	}
	for _, choice := range offered {
		value := choice[1]
		if value == "100" {
			continue
		}
		if !strings.Contains(sharedStyle, `html[data-pref-zoom="`+value+`"]{--zoom:`) {
			t.Errorf("zoom %s%% has no factor", value)
		}
		if !strings.Contains(themeBootstrap, "|"+value+"|") && !strings.Contains(themeBootstrap, "("+value+"|") && !strings.Contains(themeBootstrap, "|"+value+")") {
			t.Errorf("zoom %s%% is not applied before the page paints", value)
		}
	}
}

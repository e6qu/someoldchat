package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	now    = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	old    = now.Add(-quarantine)
	young  = old.Add(time.Microsecond)
	pinned = "sha256:" + strings.Repeat("a", 64)
	moved  = "sha256:" + strings.Repeat("b", 64)
)

func TestFindPinsNamesTheDockerHubRepositoryTheyCopy(t *testing.T) {
	found := findPins(map[string]string{
		"Dockerfile": "FROM public.ecr.aws/docker/library/golang:1.27.2-bookworm@" + pinned + " AS build",
		"ci.yml":     "driver-opts: image=mirror.gcr.io/moby/buildkit:buildx-stable-1@" + pinned,
		"other.yml":  "image: public.ecr.aws/docker/library/postgres:18-alpine",
	})
	want := []pin{
		{Registry: "public.ecr.aws/docker/library", Path: "golang", Hub: "library/golang", Tag: "1.27.2-bookworm", Digest: pinned},
		{Registry: "mirror.gcr.io", Path: "moby/buildkit", Hub: "moby/buildkit", Tag: "buildx-stable-1", Digest: pinned},
	}
	if len(found) != len(want) {
		t.Fatalf("findPins() found %v, want %v", found, want)
	}
	for _, p := range want {
		if _, ok := found[p]; !ok {
			t.Errorf("findPins() missed %+v", p)
		}
	}
}

func TestNewerReleasesKeepTheVariantAndMajorVersion(t *testing.T) {
	candidates := []release{
		{"1.27.3-bookworm", old}, {"1.27.3-alpine", old}, {"1.28.0-bookworm", old},
		{"2.0.0-bookworm", old}, {"1.27-bookworm", old}, {"1.27.1-bookworm", old},
	}
	var tags []string
	for _, r := range newerReleases("1.27.2-bookworm", candidates) {
		tags = append(tags, r.Tag)
	}
	if !slices.Equal(tags, []string{"1.28.0-bookworm", "1.27.3-bookworm"}) {
		t.Errorf("newerReleases() = %v", tags)
	}
	if got := newerReleases("buildx-stable-1", candidates); len(got) != 0 {
		t.Errorf("a tag that is not a version has newer releases %v", got)
	}
	if got := newerReleases("18.1", []release{{"18.6", old}, {"19.0", old}}); len(got) != 1 || got[0].Tag != "18.6" {
		t.Errorf("newerReleases(18.1) = %v, want 18.6 only", got)
	}
}

func TestVerdict(t *testing.T) {
	cases := []struct {
		name        string
		hub, mirror string
		hubPushed   time.Time
		newer       []release
		want        string
	}{
		{"current and mirrored", pinned, pinned, old, nil, "ok"},
		{"moved tag inside the quarantine", moved, pinned, young, nil, "HELD"},
		{"moved tag after the quarantine", moved, pinned, old, nil, "DRIFT"},
		{"newer release inside the quarantine", pinned, pinned, old, []release{{"18.6", young}}, "HELD"},
		{"newer release after the quarantine", pinned, pinned, old, []release{{"18.6", old}}, "DRIFT"},
		{"mirror serves another digest", pinned, moved, old, nil, "DRIFT"},
	}
	for _, c := range cases {
		if got, notes := verdict(pinned, c.hub, c.hubPushed, c.mirror, c.newer, now); got != c.want {
			t.Errorf("%s: verdict() = %s %v, want %s", c.name, got, notes, c.want)
		}
	}
}

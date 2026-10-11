// Command image-pins fails when a pinned container image is behind what Docker
// Hub serves for it.
//
// Every image this repository pulls is a copy of a Docker Hub image, pinned by
// tag and digest: public.ecr.aws/docker/library/<name> for official images and
// mirror.gcr.io/<repository> for the rest. A digest that never moves rots, so
// each pin is compared with Docker Hub:
//
//	ok     the pin is the digest Docker Hub serves for its tag, and the mirror
//	       serves that digest too
//	HELD   Docker Hub has something newer (a new digest for the tag, or a newer
//	       release with the same variant and major version), but it is less
//	       than 24 hours old
//	DRIFT  Docker Hub has had something newer for at least 24 hours, or the
//	       mirror serves a different digest for the tag than the pin: fails
//
// tests/dependency-admission checks that each pin matches its inventory entry
// and has aged past the quarantine; it never asks a registry whether a newer
// digest exists. ECR Public allows one unauthenticated request per second, so
// requests to it are spaced apart.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	quarantine = 24 * time.Hour
	ecrSpacing = 1100 * time.Millisecond
)

var (
	pinPattern     = regexp.MustCompile(`(public\.ecr\.aws/docker/library|mirror\.gcr\.io)/([a-z0-9._/-]+):([A-Za-z0-9_.-]+)@(sha256:[0-9a-f]{64})`)
	versionPattern = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(-[A-Za-z0-9_.-]+)?$`)
	indexTypes     = strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", ")
)

// pin is one pinned image; Hub is the Docker Hub repository it copies.
type pin struct {
	Registry, Path, Hub, Tag, Digest string
}

// release is a Docker Hub tag and the time it was last pushed.
type release struct {
	Tag    string
	Pushed time.Time
}

func findPins(files map[string]string) map[pin][]string {
	found := map[pin][]string{}
	for path, text := range files {
		for _, m := range pinPattern.FindAllStringSubmatch(text, -1) {
			hub := "library/" + m[2]
			if m[1] == "mirror.gcr.io" && strings.Contains(m[2], "/") {
				hub = m[2]
			}
			key := pin{Registry: m[1], Path: m[2], Hub: hub, Tag: m[3], Digest: m[4]}
			if !slices.Contains(found[key], path) {
				found[key] = append(found[key], path)
			}
		}
	}
	return found
}

func parseVersion(tag string) (prefix string, numbers []int, suffix string, ok bool) {
	m := versionPattern.FindStringSubmatch(tag)
	if m == nil {
		return "", nil, "", false
	}
	for _, part := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return "", nil, "", false
		}
		numbers = append(numbers, n)
	}
	return m[1], numbers, m[3], true
}

// newerReleases returns the candidates that are newer releases of the same
// major version and variant as tag, newest first. Tags that are not versions
// (latest, alpine) have no newer releases.
func newerReleases(tag string, candidates []release) []release {
	prefix, numbers, suffix, ok := parseVersion(tag)
	if !ok {
		return nil
	}
	type versioned struct {
		numbers []int
		release release
	}
	var newer []versioned
	for _, candidate := range candidates {
		p, n, s, ok := parseVersion(candidate.Tag)
		if !ok || p != prefix || s != suffix || len(n) != len(numbers) || n[0] != numbers[0] {
			continue
		}
		if slices.Compare(n, numbers) > 0 {
			newer = append(newer, versioned{n, candidate})
		}
	}
	slices.SortFunc(newer, func(a, b versioned) int { return slices.Compare(b.numbers, a.numbers) })
	result := make([]release, 0, len(newer))
	for _, v := range newer {
		result = append(result, v.release)
	}
	return result
}

// verdict classifies one pin and explains the classification.
func verdict(pinned, hubDigest string, hubPushed time.Time, mirrorDigest string, newer []release, now time.Time) (string, []string) {
	var problems, held []string
	switch {
	case hubDigest != pinned && now.Sub(hubPushed) >= quarantine:
		problems = append(problems, fmt.Sprintf("Docker Hub has served %s for the tag since %s", hubDigest, hubPushed.Format(time.RFC3339)))
	case hubDigest != pinned:
		held = append(held, fmt.Sprintf("Docker Hub moved the tag to %s at %s", hubDigest, hubPushed.Format(time.RFC3339)))
	case mirrorDigest != pinned:
		problems = append(problems, fmt.Sprintf("the mirror serves %s for the tag, not the pinned digest", mirrorDigest))
	}
	var eligible []release
	for _, r := range newer {
		if now.Sub(r.Pushed) >= quarantine {
			eligible = append(eligible, r)
		}
	}
	switch {
	case len(eligible) > 0:
		problems = append(problems, fmt.Sprintf("newer release %s has been on Docker Hub since %s", eligible[0].Tag, eligible[0].Pushed.Format(time.RFC3339)))
	case len(newer) > 0:
		held = append(held, fmt.Sprintf("newer release %s is inside the 24-hour quarantine", newer[0].Tag))
	}
	if len(problems) > 0 {
		return "DRIFT", problems
	}
	if len(held) > 0 {
		return "HELD", held
	}
	return "ok", nil
}

type registry struct {
	client  *http.Client
	lastECR time.Time
}

func (r *registry) do(method, address string, header http.Header, ecr bool) (*http.Response, error) {
	if ecr {
		if wait := time.Until(r.lastECR.Add(ecrSpacing)); wait > 0 {
			time.Sleep(wait)
		}
		defer func() { r.lastECR = time.Now() }()
	}
	request, err := http.NewRequest(method, address, nil)
	if err != nil {
		return nil, err
	}
	for key, values := range header {
		request.Header[key] = values
	}
	request.Header.Set("User-Agent", "sameoldchat-image-pins")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("%s %s: HTTP %d", method, address, response.StatusCode)
	}
	return response, nil
}

func (r *registry) json(address string, ecr bool, into any) error {
	response, err := r.do(http.MethodGet, address, nil, ecr)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return json.NewDecoder(response.Body).Decode(into)
}

func (r *registry) hubTag(p pin) (string, time.Time, error) {
	var tag struct {
		Digest        string `json:"digest"`
		TagLastPushed string `json:"tag_last_pushed"`
	}
	if err := r.json("https://hub.docker.com/v2/repositories/"+p.Hub+"/tags/"+url.PathEscape(p.Tag), false, &tag); err != nil {
		return "", time.Time{}, err
	}
	pushed, err := time.Parse(time.RFC3339Nano, tag.TagLastPushed)
	if err != nil || tag.Digest == "" {
		return "", time.Time{}, fmt.Errorf("Docker Hub returned no digest or push time for %s:%s", p.Hub, p.Tag)
	}
	return tag.Digest, pushed, nil
}

func (r *registry) hubCandidates(p pin) ([]release, error) {
	prefix, numbers, _, ok := parseVersion(p.Tag)
	if !ok {
		return nil, nil
	}
	name := url.QueryEscape(prefix + strconv.Itoa(numbers[0]) + ".")
	var page struct {
		Results []struct {
			Name          string `json:"name"`
			TagLastPushed string `json:"tag_last_pushed"`
		} `json:"results"`
	}
	if err := r.json("https://hub.docker.com/v2/repositories/"+p.Hub+"/tags?page_size=100&ordering=last_updated&name="+name, false, &page); err != nil {
		return nil, err
	}
	var result []release
	for _, row := range page.Results {
		pushed, err := time.Parse(time.RFC3339Nano, row.TagLastPushed)
		if err != nil {
			continue // a tag with no push time has never been pushed
		}
		result = append(result, release{Tag: row.Name, Pushed: pushed})
	}
	return result, nil
}

func (r *registry) mirrorDigest(p pin) (string, error) {
	header := http.Header{"Accept": {indexTypes}}
	ecr := p.Registry == "public.ecr.aws/docker/library"
	manifest := "https://mirror.gcr.io/v2/" + p.Path + "/manifests/" + p.Tag
	if ecr {
		var token struct {
			Token string `json:"token"`
		}
		scope := url.QueryEscape("repository:docker/library/" + p.Path + ":pull")
		if err := r.json("https://public.ecr.aws/token/?scope="+scope+"&service=public.ecr.aws", true, &token); err != nil {
			return "", err
		}
		header.Set("Authorization", "Bearer "+token.Token)
		manifest = "https://public.ecr.aws/v2/docker/library/" + p.Path + "/manifests/" + p.Tag
	}
	response, err := r.do(http.MethodHead, manifest, header, ecr)
	if err != nil {
		return "", err
	}
	response.Body.Close()
	digest := response.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("%s returned no digest for %s:%s", p.Registry, p.Path, p.Tag)
	}
	return digest, nil
}

func trackedFiles() (map[string]string, error) {
	out, err := exec.Command("git", "grep", "-lE", `(public\.ecr\.aws/docker/library|mirror\.gcr\.io)/[a-z0-9._/-]+:[A-Za-z0-9_.-]+@sha256:`, "--", ":!*.md").Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("git grep: %w", err)
	}
	files := map[string]string{}
	for _, path := range strings.Fields(string(out)) {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files[path] = string(body)
	}
	return files, nil
}

func main() {
	files, err := trackedFiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	found := findPins(files)
	if len(found) == 0 {
		fmt.Fprintln(os.Stderr, "no pinned images found; the check is not looking at anything")
		os.Exit(1)
	}
	keys := make([]pin, 0, len(found))
	for key := range found {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b pin) int {
		return strings.Compare(a.Registry+a.Path+":"+a.Tag+a.Digest, b.Registry+b.Path+":"+b.Tag+b.Digest)
	})
	r := &registry{client: &http.Client{Timeout: 20 * time.Second}}
	now := time.Now().UTC()
	failed := 0
	for _, p := range keys {
		where := found[p]
		slices.Sort(where)
		label := fmt.Sprintf("%s/%s:%s (%s)", p.Registry, p.Path, p.Tag, strings.Join(where, ", "))
		hubDigest, hubPushed, err := r.hubTag(p)
		var mirrored string
		var candidates []release
		if err == nil {
			mirrored, err = r.mirrorDigest(p)
		}
		if err == nil {
			candidates, err = r.hubCandidates(p)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR  %s: could not be checked: %v\n", label, err)
			failed++
			continue
		}
		state, notes := verdict(p.Digest, hubDigest, hubPushed, mirrored, newerReleases(p.Tag, candidates), now)
		fmt.Printf("%-6s %s\n", state, label)
		for _, note := range notes {
			fmt.Printf("         %s\n", note)
		}
		if state == "DRIFT" {
			failed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "%d image pin(s) are behind Docker Hub or could not be checked. Bump each one to the digest Docker Hub serves once the mirror serves the same digest, and update specs/dependency-admission.yaml with it.\n", failed)
		os.Exit(1)
	}
	fmt.Printf("All %d pinned images are current or held.\n", len(found))
}

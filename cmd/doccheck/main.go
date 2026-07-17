package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

func main() {
	root := "."
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "doccheck: usage: doccheck [root]")
		os.Exit(2)
	}
	if len(os.Args) == 2 {
		root = os.Args[1]
	}
	if err := checkTree(root); err != nil {
		fmt.Fprintln(os.Stderr, "doccheck:", err)
		os.Exit(1)
	}
}

func checkTree(root string) error {
	files := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == ".cache" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, path := range files {
		if err := checkFile(path); err != nil {
			return err
		}
	}
	return nil
}

func checkFile(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, match := range markdownLink.FindAllStringSubmatch(string(body), -1) {
		target := strings.TrimSpace(match[1])
		if target == "" || strings.HasPrefix(target, "#") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		if hash := strings.IndexByte(target, '#'); hash >= 0 {
			target = target[:hash]
		}
		if query := strings.IndexByte(target, '?'); query >= 0 {
			target = target[:query]
		}
		if target == "" {
			continue
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
		if _, err := os.Stat(resolved); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%s links to missing path %q", path, target)
			}
			return fmt.Errorf("%s checks link %q: %w", path, target, err)
		}
	}
	return nil
}

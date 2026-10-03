package store

import (
	"sort"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The matching and order every profile applies to a remote file search, so
// the SQL and memory profiles cannot disagree about what a search found.

// SearchTextMatches reports folded text that holds every term and none of the
// excluded ones.
func SearchTextMatches(text string, terms, excluded []string) bool {
	for _, term := range terms {
		if !strings.Contains(text, domain.FoldSearchText(term)) {
			return false
		}
	}
	for _, term := range excluded {
		if strings.Contains(text, domain.FoldSearchText(term)) {
			return false
		}
	}
	return true
}

// RemoteFileMatchesType matches a type: search modifier against the file type
// the app declared, or the extension of the title.
func RemoteFileMatchesType(file domain.RemoteFile, wanted string) bool {
	wanted = strings.TrimPrefix(domain.FoldSearchText(wanted), ".")
	return domain.FoldSearchText(file.FileType) == wanted || strings.HasSuffix(domain.FoldSearchText(file.Title), "."+wanted)
}

// SortRemoteFiles orders remote files by when they were added, then by ID,
// as file search orders hosted files.
func SortRemoteFiles(values []domain.RemoteFile, direction domain.SearchDirection) {
	sort.Slice(values, func(left, right int) bool {
		if values[left].CreatedAt.Equal(values[right].CreatedAt) {
			if direction == domain.SearchDirectionDescending {
				return values[left].ID > values[right].ID
			}
			return values[left].ID < values[right].ID
		}
		if direction == domain.SearchDirectionDescending {
			return values[left].CreatedAt.After(values[right].CreatedAt)
		}
		return values[left].CreatedAt.Before(values[right].CreatedAt)
	})
}

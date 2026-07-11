package importers

import (
	"context"
	"encoding/csv"
	"io"
	"strings"

	"github.com/tiagojct/scrimshaw/internal/store"
)

// Instapaper imports its CSV export. It recognizes URL, Title, Folder, and
// Starred headers case-insensitively; unknown columns are intentionally ignored.
func Instapaper(ctx context.Context, input io.Reader, destination *store.Store) (int, error) {
	rows, err := csv.NewReader(io.LimitReader(input, 10<<20)).ReadAll()
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	columns := make(map[string]int, len(rows[0]))
	for index, name := range rows[0] {
		columns[strings.ToLower(strings.TrimSpace(name))] = index
	}
	urlColumn, ok := columns["url"]
	if !ok {
		return 0, nil
	}
	titleColumn, hasTitle := columns["title"]
	folderColumn, hasFolder := columns["folder"]
	starredColumn, hasStarred := columns["starred"]
	var imported int
	for _, row := range rows[1:] {
		if urlColumn >= len(row) || strings.TrimSpace(row[urlColumn]) == "" {
			continue
		}
		title := ""
		if hasTitle && titleColumn < len(row) {
			title = strings.TrimSpace(row[titleColumn])
		}
		var tags []string
		if hasFolder && folderColumn < len(row) && strings.TrimSpace(row[folderColumn]) != "" {
			tags = []string{strings.TrimSpace(row[folderColumn])}
		}
		id, err := destination.InsertManualItem(ctx, row[urlColumn], title, "", "", "", tags, true)
		if err != nil {
			continue
		}
		if hasStarred && starredColumn < len(row) && isTrue(row[starredColumn]) {
			if err := destination.SetStarred(ctx, id, true); err != nil {
				return imported, err
			}
		}
		imported++
	}
	return imported, nil
}

func isTrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "starred":
		return true
	default:
		return false
	}
}

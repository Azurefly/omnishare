package storage

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	FolderRootFilter      = "__root__"
	FolderKindNotes       = "notes"
	FolderKindFiles       = "files"
	FolderKindPads        = "pads"
	MaxFolderDepth        = 6
	MaxFolderSegmentRunes = 64
	MaxFolderPathBytes    = 240
)

// ValidFolderKind normalizes singular/plural resource names into a folder kind.
func ValidFolderKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "note", "notes":
		return FolderKindNotes, nil
	case "file", "files":
		return FolderKindFiles, nil
	case "pad", "pads":
		return FolderKindPads, nil
	default:
		return "", errors.New("invalid folder kind")
	}
}

// NormalizeFolderPath cleans a virtual folder path such as "工作/方案". An empty
// string represents the root / unfiled directory. Traversal and invalid chars fail.
func NormalizeFolderPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	normalized := strings.ReplaceAll(trimmed, `\`, "/")
	normalized = strings.Trim(normalized, "/")
	if normalized == "" {
		return "", nil
	}
	parts := strings.Split(normalized, "/")
	if len(parts) > MaxFolderDepth {
		return "", errors.New("folder depth exceeds maximum of 6 levels")
	}
	out := make([]string, 0, len(parts))
	for _, seg := range parts {
		seg = strings.TrimSpace(seg)
		if seg == "" || seg == "." || seg == ".." || seg == FolderRootFilter {
			return "", errors.New("invalid folder path segment")
		}
		if utf8.RuneCountInString(seg) > MaxFolderSegmentRunes {
			return "", errors.New("folder name is too long")
		}
		if strings.ContainsAny(seg, `<>:"|?*`) {
			return "", errors.New("folder name contains unsupported characters")
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return "", errors.New("folder name contains control characters")
			}
		}
		out = append(out, seg)
	}
	joined := strings.Join(out, "/")
	if len([]byte(joined)) > MaxFolderPathBytes {
		return "", errors.New("folder path exceeds maximum length")
	}
	return joined, nil
}

// FolderAncestors returns all non-empty ancestor paths from top to leaf.
func FolderAncestors(folderPath string) []string {
	if folderPath == "" {
		return nil
	}
	parts := strings.Split(folderPath, "/")
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

// MatchFolder checks whether an item's folder matches a query filter.
func MatchFolder(itemFolder, filterFolder string, recursive bool) bool {
	filter := strings.TrimSpace(filterFolder)
	if filter == "" {
		return true
	}
	if filter == FolderRootFilter {
		return itemFolder == ""
	}
	norm, err := NormalizeFolderPath(filter)
	if err != nil {
		return false
	}
	if norm == "" {
		return itemFolder == ""
	}
	if recursive {
		return itemFolder == norm || strings.HasPrefix(itemFolder, norm+"/")
	}
	return itemFolder == norm
}

// RewriteFolderPrefix replaces oldPath (and any nested descendants) with newPath.
func RewriteFolderPrefix(current, oldPath, newPath string) (string, bool) {
	if current == oldPath {
		return newPath, true
	}
	prefix := oldPath + "/"
	if strings.HasPrefix(current, prefix) {
		return newPath + "/" + strings.TrimPrefix(current, prefix), true
	}
	return current, false
}

// FolderInSubtree checks whether current equals path or sits under path/.
func FolderInSubtree(current, path string) bool {
	return current == path || strings.HasPrefix(current, path+"/")
}

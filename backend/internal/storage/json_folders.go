package storage

import (
	"errors"
	"sort"
	"strings"
	"time"

	"omnishare/internal/model"
)

func ensureStateFolderChain(st *state, kind, folderPath string, now time.Time) error {
	normKind, err := ValidFolderKind(kind)
	if err != nil {
		return err
	}
	normPath, err := NormalizeFolderPath(folderPath)
	if err != nil {
		return err
	}
	if normPath == "" {
		return nil
	}
	existing := map[string]bool{}
	for _, entry := range st.Folders {
		if entry.Kind == normKind {
			existing[entry.Path] = true
		}
	}
	for _, ancestor := range FolderAncestors(normPath) {
		if !existing[ancestor] {
			st.Folders = append(st.Folders, model.FolderEntry{
				Kind:      normKind,
				Path:      ancestor,
				CreatedAt: now,
				UpdatedAt: now,
			})
			existing[ancestor] = true
		}
	}
	return nil
}

func (s *Store) ListFolders(kind string) ([]model.FolderNode, error) {
	normKind, err := ValidFolderKind(kind)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	counts := map[string]int{}
	paths := map[string]struct{}{}

	for _, f := range s.state.Folders {
		if f.Kind == normKind {
			if norm, normErr := NormalizeFolderPath(f.Path); normErr == nil && norm != "" {
				for _, a := range FolderAncestors(norm) {
					paths[a] = struct{}{}
				}
			}
		}
	}

	recordItem := func(folder string) {
		norm, normErr := NormalizeFolderPath(folder)
		if normErr != nil || norm == "" {
			return
		}
		counts[norm]++
		for _, a := range FolderAncestors(norm) {
			paths[a] = struct{}{}
		}
	}

	switch normKind {
	case FolderKindNotes:
		for _, n := range s.state.Notes {
			if n.DeletedAt == nil && !expired(n.ExpiresAt, now) {
				recordItem(n.Folder)
			}
		}
	case FolderKindFiles:
		for _, f := range s.state.Files {
			if f.DeletedAt == nil && !expired(f.ExpiresAt, now) {
				recordItem(f.Folder)
			}
		}
	case FolderKindPads:
		for _, p := range s.state.Pads {
			if p.DeletedAt == nil {
				recordItem(p.Folder)
			}
		}
	}

	out := make([]model.FolderNode, 0, len(paths))
	for p := range paths {
		out = append(out, model.FolderNode{Kind: normKind, Path: p, ItemCount: counts[p]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *Store) EnsureFolder(kind, path string) error {
	normKind, err := ValidFolderKind(kind)
	if err != nil {
		return err
	}
	normPath, err := NormalizeFolderPath(path)
	if err != nil {
		return err
	}
	if normPath == "" {
		return errors.New("folder path is required")
	}
	return s.mutate(func(st *state) error {
		now := time.Now()
		if err := ensureStateFolderChain(st, normKind, normPath, now); err != nil {
			return err
		}
		appendAudit(st, "create", "folder", normKind+":"+normPath, "folder created", map[string]interface{}{"kind": normKind, "path": normPath})
		return nil
	})
}

func (s *Store) RenameFolder(kind, oldPath, newPath string) (int, error) {
	normKind, err := ValidFolderKind(kind)
	if err != nil {
		return 0, err
	}
	oldNorm, err := NormalizeFolderPath(oldPath)
	if err != nil || oldNorm == "" {
		return 0, errors.New("valid source folder path is required")
	}
	newNorm, err := NormalizeFolderPath(newPath)
	if err != nil || newNorm == "" {
		return 0, errors.New("valid target folder path is required")
	}
	if oldNorm == newNorm {
		return 0, errors.New("target folder path is unchanged")
	}
	if FolderInSubtree(newNorm, oldNorm) {
		return 0, errors.New("cannot move a folder inside itself")
	}

	moved := 0
	err = s.mutate(func(st *state) error {
		now := time.Now()
		if err := ensureStateFolderChain(st, normKind, newNorm, now); err != nil {
			return err
		}
		seen := map[string]bool{}
		nextFolders := make([]model.FolderEntry, 0, len(st.Folders))
		for _, entry := range st.Folders {
			if entry.Kind == normKind {
				if nextPath, ok := RewriteFolderPrefix(entry.Path, oldNorm, newNorm); ok {
					entry.Path = nextPath
					entry.UpdatedAt = now
				}
			}
			key := entry.Kind + "\x00" + entry.Path
			if !seen[key] {
				seen[key] = true
				nextFolders = append(nextFolders, entry)
			}
		}
		st.Folders = nextFolders

		switch normKind {
		case FolderKindNotes:
			for i := range st.Notes {
				if st.Notes[i].DeletedAt != nil {
					continue
				}
				if nextF, ok := RewriteFolderPrefix(st.Notes[i].Folder, oldNorm, newNorm); ok {
					if _, err := NormalizeFolderPath(nextF); err != nil {
						return err
					}
					st.Notes[i].Folder = nextF
					st.Notes[i].UpdatedAt = now
					moved++
				}
			}
		case FolderKindFiles:
			for i := range st.Files {
				if st.Files[i].DeletedAt != nil {
					continue
				}
				if nextF, ok := RewriteFolderPrefix(st.Files[i].Folder, oldNorm, newNorm); ok {
					if _, err := NormalizeFolderPath(nextF); err != nil {
						return err
					}
					st.Files[i].Folder = nextF
					moved++
				}
			}
		case FolderKindPads:
			for i := range st.Pads {
				if st.Pads[i].DeletedAt != nil {
					continue
				}
				if nextF, ok := RewriteFolderPrefix(st.Pads[i].Folder, oldNorm, newNorm); ok {
					if _, err := NormalizeFolderPath(nextF); err != nil {
						return err
					}
					st.Pads[i].Folder = nextF
					st.Pads[i].Version++
					st.Pads[i].UpdatedAt = now
					moved++
				}
			}
		}
		appendAudit(st, "rename", "folder", normKind+":"+oldNorm, "folder renamed", map[string]interface{}{"kind": normKind, "old_path": oldNorm, "new_path": newNorm, "moved_items": moved})
		return nil
	})
	return moved, err
}

func (s *Store) DeleteFolder(kind, path, mode string) (int, error) {
	normKind, err := ValidFolderKind(kind)
	if err != nil {
		return 0, err
	}
	normPath, err := NormalizeFolderPath(path)
	if err != nil || normPath == "" {
		return 0, errors.New("valid folder path is required")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "move_to_root"
	}
	if mode != "move_to_root" && mode != "trash_items" {
		return 0, errors.New("invalid folder delete mode")
	}

	affected := 0
	err = s.mutate(func(st *state) error {
		now := time.Now()
		nextFolders := make([]model.FolderEntry, 0, len(st.Folders))
		for _, entry := range st.Folders {
			if entry.Kind == normKind && FolderInSubtree(entry.Path, normPath) {
				continue
			}
			nextFolders = append(nextFolders, entry)
		}
		st.Folders = nextFolders

		switch normKind {
		case FolderKindNotes:
			for i := range st.Notes {
				if st.Notes[i].DeletedAt == nil && FolderInSubtree(st.Notes[i].Folder, normPath) {
					if mode == "trash_items" {
						t := now
						st.Notes[i].DeletedAt = &t
					} else {
						st.Notes[i].Folder = ""
						st.Notes[i].UpdatedAt = now
					}
					affected++
				}
			}
		case FolderKindFiles:
			for i := range st.Files {
				if st.Files[i].DeletedAt == nil && FolderInSubtree(st.Files[i].Folder, normPath) {
					if mode == "trash_items" {
						t := now
						st.Files[i].DeletedAt = &t
					} else {
						st.Files[i].Folder = ""
					}
					affected++
				}
			}
		case FolderKindPads:
			for i := range st.Pads {
				if st.Pads[i].DeletedAt == nil && FolderInSubtree(st.Pads[i].Folder, normPath) {
					if mode == "trash_items" {
						t := now
						st.Pads[i].DeletedAt = &t
					} else {
						st.Pads[i].Folder = ""
						st.Pads[i].Version++
						st.Pads[i].UpdatedAt = now
					}
					affected++
				}
			}
		}
		appendAudit(st, "delete", "folder", normKind+":"+normPath, "folder deleted", map[string]interface{}{"kind": normKind, "path": normPath, "mode": mode, "affected_items": affected})
		return nil
	})
	return affected, err
}

func (s *Store) MoveItems(kind string, ids []string, folder string) (int, error) {
	normKind, err := ValidFolderKind(kind)
	if err != nil {
		return 0, err
	}
	normFolder, err := NormalizeFolderPath(folder)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, errors.New("ids are required")
	}

	target := map[string]bool{}
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id != "" {
			target[id] = true
		}
	}
	moved := 0
	err = s.mutate(func(st *state) error {
		now := time.Now()
		if err := ensureStateFolderChain(st, normKind, normFolder, now); err != nil {
			return err
		}
		switch normKind {
		case FolderKindNotes:
			for i := range st.Notes {
				if st.Notes[i].DeletedAt == nil && target[st.Notes[i].ID] {
					st.Notes[i].Folder = normFolder
					st.Notes[i].UpdatedAt = now
					moved++
				}
			}
		case FolderKindFiles:
			for i := range st.Files {
				if st.Files[i].DeletedAt == nil && target[st.Files[i].ID] {
					st.Files[i].Folder = normFolder
					moved++
				}
			}
		case FolderKindPads:
			for i := range st.Pads {
				if st.Pads[i].DeletedAt == nil && target[st.Pads[i].ID] {
					st.Pads[i].Folder = normFolder
					st.Pads[i].Version++
					st.Pads[i].UpdatedAt = now
					moved++
				}
			}
		}
		appendAudit(st, "move", normKind, normFolder, "items moved to folder", map[string]interface{}{"kind": normKind, "folder": normFolder, "count": moved})
		return nil
	})
	return moved, err
}

package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"omnishare/internal/model"
)

func ensureFolderSchemaTx(tx *sql.Tx) error {
	for _, spec := range []struct {
		table string
		index string
	}{
		{"quick_notes", "idx_notes_folder"},
		{"file_assets", "idx_files_folder"},
		{"pad_documents", "idx_pads_folder"},
	} {
		hasCol, err := tableHasColumnTx(tx, spec.table, "folder")
		if err != nil {
			return err
		}
		if !hasCol {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN folder TEXT NOT NULL DEFAULT ''", spec.table)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s(folder)", spec.index, spec.table)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS workspace_folders (
		kind TEXT NOT NULL,
		path TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (kind, path)
	)`); err != nil {
		return err
	}
	return nil
}

func tableHasColumnTx(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func ensureFolderChainTx(tx *sql.Tx, kind, folderPath string, now time.Time) error {
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
	nowStr := now.UTC().Format(time.RFC3339Nano)
	for _, ancestor := range FolderAncestors(normPath) {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO workspace_folders(kind, path, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			normKind, ancestor, nowStr, nowStr); err != nil {
			return err
		}
	}
	return nil
}

func folderTableSpec(kind string) (normKind, table string, err error) {
	normKind, err = ValidFolderKind(kind)
	if err != nil {
		return "", "", err
	}
	switch normKind {
	case FolderKindNotes:
		return normKind, "quick_notes", nil
	case FolderKindFiles:
		return normKind, "file_assets", nil
	case FolderKindPads:
		return normKind, "pad_documents", nil
	default:
		return "", "", errors.New("invalid folder kind")
	}
}

func (s *SQLiteStore) ListFolders(kind string) ([]model.FolderNode, error) {
	normKind, table, err := folderTableSpec(kind)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	counts := map[string]int{}
	paths := map[string]struct{}{}

	rows, err := s.readDB.Query(`SELECT path FROM workspace_folders WHERE kind = ?`, normKind)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				if norm, normErr := NormalizeFolderPath(p); normErr == nil && norm != "" {
					for _, a := range FolderAncestors(norm) {
						paths[a] = struct{}{}
					}
				}
			}
		}
	}

	var itemQuery string
	if normKind == FolderKindPads {
		itemQuery = fmt.Sprintf("SELECT folder, '' FROM %s WHERE deleted_at IS NULL", table)
	} else {
		itemQuery = fmt.Sprintf("SELECT folder, expires_at FROM %s WHERE deleted_at IS NULL", table)
	}
	itemRows, err := s.readDB.Query(itemQuery)
	if err != nil {
		return nil, err
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var folder string
		var exp sql.NullString
		if itemRows.Scan(&folder, &exp) != nil {
			continue
		}
		if exp.Valid && exp.String != "" && expired(parseTime(&exp.String), now) {
			continue
		}
		norm, normErr := NormalizeFolderPath(folder)
		if normErr != nil || norm == "" {
			continue
		}
		counts[norm]++
		for _, a := range FolderAncestors(norm) {
			paths[a] = struct{}{}
		}
	}

	out := make([]model.FolderNode, 0, len(paths))
	for p := range paths {
		out = append(out, model.FolderNode{Kind: normKind, Path: p, ItemCount: counts[p]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *SQLiteStore) EnsureFolder(kind, path string) error {
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	if err := ensureFolderChainTx(tx, normKind, normPath, now); err != nil {
		return err
	}
	if err := s.appendAuditTx(tx, "create", "folder", normKind+":"+normPath, "folder created", map[string]interface{}{"kind": normKind, "path": normPath}); err != nil {
		return err
	}
	if err := s.commit(tx); err != nil {
		return err
	}
	tx = nil
	return nil
}

func (s *SQLiteStore) RenameFolder(kind, oldPath, newPath string) (int, error) {
	normKind, table, err := folderTableSpec(kind)
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	if err := ensureFolderChainTx(tx, normKind, newNorm, now); err != nil {
		return 0, err
	}

	fRows, err := tx.Query(`SELECT path, created_at FROM workspace_folders WHERE kind = ?`, normKind)
	if err != nil {
		return 0, err
	}
	type folderRow struct {
		oldP, newP, createdAt string
	}
	moves := []folderRow{}
	for fRows.Next() {
		var p, createdAt string
		if fRows.Scan(&p, &createdAt) == nil {
			if nextP, ok := RewriteFolderPrefix(p, oldNorm, newNorm); ok {
				moves = append(moves, folderRow{p, nextP, createdAt})
			}
		}
	}
	_ = fRows.Close()

	for _, mv := range moves {
		if _, err := tx.Exec(`DELETE FROM workspace_folders WHERE kind = ? AND path = ?`, normKind, mv.oldP); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO workspace_folders(kind, path, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			normKind, mv.newP, mv.createdAt, nowStr); err != nil {
			return 0, err
		}
	}

	itemRows, err := tx.Query(fmt.Sprintf(`SELECT id, folder FROM %s WHERE deleted_at IS NULL AND folder != ''`, table))
	if err != nil {
		return 0, err
	}
	type itemMove struct{ id, newFolder string }
	itemMoves := []itemMove{}
	for itemRows.Next() {
		var id, folder string
		if itemRows.Scan(&id, &folder) == nil {
			if nextFolder, ok := RewriteFolderPrefix(folder, oldNorm, newNorm); ok {
				if _, err := NormalizeFolderPath(nextFolder); err != nil {
					_ = itemRows.Close()
					return 0, err
				}
				itemMoves = append(itemMoves, itemMove{id, nextFolder})
			}
		}
	}
	_ = itemRows.Close()

	for _, im := range itemMoves {
		var q string
		var args []interface{}
		switch normKind {
		case FolderKindNotes:
			q = `UPDATE quick_notes SET folder = ?, updated_at = ? WHERE id = ?`
			args = []interface{}{im.newFolder, nowStr, im.id}
		case FolderKindFiles:
			q = `UPDATE file_assets SET folder = ? WHERE id = ?`
			args = []interface{}{im.newFolder, im.id}
		case FolderKindPads:
			q = `UPDATE pad_documents SET folder = ?, version = version + 1, updated_at = ? WHERE id = ?`
			args = []interface{}{im.newFolder, nowStr, im.id}
		}
		if _, err := tx.Exec(q, args...); err != nil {
			return 0, err
		}
	}

	if err := s.appendAuditTx(tx, "rename", "folder", normKind+":"+oldNorm, "folder renamed", map[string]interface{}{"kind": normKind, "old_path": oldNorm, "new_path": newNorm, "moved_items": len(itemMoves)}); err != nil {
		return 0, err
	}
	if err := s.commit(tx); err != nil {
		return 0, err
	}
	tx = nil
	return len(itemMoves), nil
}

func (s *SQLiteStore) DeleteFolder(kind, path, mode string) (int, error) {
	normKind, table, err := folderTableSpec(kind)
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	fRows, err := tx.Query(`SELECT path FROM workspace_folders WHERE kind = ?`, normKind)
	if err != nil {
		return 0, err
	}
	dropFolders := []string{}
	for fRows.Next() {
		var p string
		if fRows.Scan(&p) == nil && FolderInSubtree(p, normPath) {
			dropFolders = append(dropFolders, p)
		}
	}
	_ = fRows.Close()
	for _, p := range dropFolders {
		if _, err := tx.Exec(`DELETE FROM workspace_folders WHERE kind = ? AND path = ?`, normKind, p); err != nil {
			return 0, err
		}
	}

	itemRows, err := tx.Query(fmt.Sprintf(`SELECT id, folder FROM %s WHERE deleted_at IS NULL AND folder != ''`, table))
	if err != nil {
		return 0, err
	}
	targetIDs := []string{}
	for itemRows.Next() {
		var id, folder string
		if itemRows.Scan(&id, &folder) == nil && FolderInSubtree(folder, normPath) {
			targetIDs = append(targetIDs, id)
		}
	}
	_ = itemRows.Close()

	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range targetIDs {
		var q string
		var args []interface{}
		if mode == "trash_items" {
			q = fmt.Sprintf(`UPDATE %s SET deleted_at = ? WHERE id = ?`, table)
			args = []interface{}{nowStr, id}
		} else {
			switch normKind {
			case FolderKindNotes:
				q = `UPDATE quick_notes SET folder = '', updated_at = ? WHERE id = ?`
				args = []interface{}{nowStr, id}
			case FolderKindFiles:
				q = `UPDATE file_assets SET folder = '' WHERE id = ?`
				args = []interface{}{id}
			case FolderKindPads:
				q = `UPDATE pad_documents SET folder = '', version = version + 1, updated_at = ? WHERE id = ?`
				args = []interface{}{nowStr, id}
			}
		}
		if _, err := tx.Exec(q, args...); err != nil {
			return 0, err
		}
	}

	if err := s.appendAuditTx(tx, "delete", "folder", normKind+":"+normPath, "folder deleted", map[string]interface{}{"kind": normKind, "path": normPath, "mode": mode, "affected_items": len(targetIDs)}); err != nil {
		return 0, err
	}
	if err := s.commit(tx); err != nil {
		return 0, err
	}
	tx = nil
	return len(targetIDs), nil
}

func (s *SQLiteStore) MoveItems(kind string, ids []string, folder string) (int, error) {
	normKind, _, err := folderTableSpec(kind)
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	if err := ensureFolderChainTx(tx, normKind, normFolder, now); err != nil {
		return 0, err
	}

	moved := 0
	seen := map[string]bool{}
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var q string
		var args []interface{}
		switch normKind {
		case FolderKindNotes:
			q = `UPDATE quick_notes SET folder = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
			args = []interface{}{normFolder, nowStr, id}
		case FolderKindFiles:
			q = `UPDATE file_assets SET folder = ? WHERE id = ? AND deleted_at IS NULL`
			args = []interface{}{normFolder, id}
		case FolderKindPads:
			q = `UPDATE pad_documents SET folder = ?, version = version + 1, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
			args = []interface{}{normFolder, nowStr, id}
		}
		res, execErr := tx.Exec(q, args...)
		if execErr != nil {
			return 0, execErr
		}
		if n, _ := res.RowsAffected(); n > 0 {
			moved += int(n)
		}
	}

	if err := s.appendAuditTx(tx, "move", normKind, normFolder, "items moved to folder", map[string]interface{}{"kind": normKind, "folder": normFolder, "count": moved}); err != nil {
		return 0, err
	}
	if err := s.commit(tx); err != nil {
		return 0, err
	}
	tx = nil
	return moved, nil
}

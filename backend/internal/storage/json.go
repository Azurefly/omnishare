package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"omnishare/internal/durable"
	"omnishare/internal/model"
)

const currentStateSchema = 2

var (
	ErrNotFound        = errors.New("record not found")
	ErrShareExpired    = errors.New("share link expired")
	ErrShareRevoked    = errors.New("share link revoked")
	ErrShareExhausted  = errors.New("share link access limit reached")
	ErrVersionConflict = errors.New("version conflict")
)

type state struct {
	SchemaVersion int                 `json:"schema_version"`
	Notes         []model.QuickNote   `json:"notes"`
	Files         []model.FileAsset   `json:"files"`
	Pads          []model.PadDocument `json:"pads"`
	Shares        []model.ShareLink   `json:"shares"`
	Audits        []model.AuditEvent  `json:"audits"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	dataDir string
	state   state
}

func New(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "uploads", "blobs"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "uploads", ".staging"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "uploads", ".purging"), 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "omnishare.json"), dataDir: dataDir}
	loaded, recovered, err := s.load()
	if err != nil {
		return nil, err
	}
	s.initializeState()
	if recovered || !loaded || s.state.SchemaVersion != currentStateSchema {
		s.state.SchemaVersion = currentStateSchema
		s.rebuildAuditChainLocked()
		if err := s.persistCandidate(s.state); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) DataDir() string { return s.dataDir }

func (s *Store) load() (loaded, recovered bool, err error) {
	data, readErr := os.ReadFile(s.path)
	if readErr == nil {
		if json.Unmarshal(data, &s.state) == nil {
			return true, false, nil
		}
	}
	backup, backupErr := os.ReadFile(s.path + ".bak")
	if backupErr == nil {
		if unmarshalErr := json.Unmarshal(backup, &s.state); unmarshalErr == nil {
			return true, true, nil
		}
	}
	if errors.Is(readErr, os.ErrNotExist) && errors.Is(backupErr, os.ErrNotExist) {
		return false, false, nil
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return false, false, readErr
	}
	return false, false, errors.New("omnishare.json is corrupt and no valid backup exists")
}

func (s *Store) initializeState() {
	if s.state.Notes == nil {
		s.state.Notes = []model.QuickNote{}
	}
	if s.state.Files == nil {
		s.state.Files = []model.FileAsset{}
	}
	if s.state.Pads == nil {
		s.state.Pads = []model.PadDocument{}
	}
	if s.state.Shares == nil {
		s.state.Shares = []model.ShareLink{}
	}
	if s.state.Audits == nil {
		s.state.Audits = []model.AuditEvent{}
	}
	if s.state.SchemaVersion <= 0 {
		s.state.SchemaVersion = currentStateSchema
	}
}

func (s *Store) persistCandidate(candidate state) error {
	candidate.SchemaVersion = currentStateSchema
	return durable.WriteFileFunc(s.path, 0o600, func(w io.Writer) error {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(candidate)
	})
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	value := *t
	return &value
}

func cloneMetadataValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMetadata(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = cloneMetadataValue(item)
		}
		return out
	case []string:
		if typed == nil {
			return nil
		}
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func cloneMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	out := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		out[key] = cloneMetadataValue(value)
	}
	return out
}

// cloneState performs a structural deep copy instead of a JSON round-trip. The
// previous marshal/unmarshal implementation serialized the entire dataset twice
// for every mutation (once to clone, once to persist), which dominated write
// latency for large states. Every reference type reachable from state must be
// copied here so rollback semantics stay intact.
func cloneState(src state) state {
	out := state{SchemaVersion: src.SchemaVersion}
	if src.Notes != nil {
		out.Notes = make([]model.QuickNote, len(src.Notes))
		for i, n := range src.Notes {
			n.Tags = append([]string(nil), n.Tags...)
			n.ExpiresAt = cloneTime(n.ExpiresAt)
			n.DeletedAt = cloneTime(n.DeletedAt)
			out.Notes[i] = n
		}
	}
	if src.Files != nil {
		out.Files = make([]model.FileAsset, len(src.Files))
		for i, f := range src.Files {
			f.ExpiresAt = cloneTime(f.ExpiresAt)
			f.DeletedAt = cloneTime(f.DeletedAt)
			out.Files[i] = f
		}
	}
	if src.Pads != nil {
		out.Pads = make([]model.PadDocument, len(src.Pads))
		for i, p := range src.Pads {
			p.DeletedAt = cloneTime(p.DeletedAt)
			out.Pads[i] = p
		}
	}
	if src.Shares != nil {
		out.Shares = make([]model.ShareLink, len(src.Shares))
		for i, link := range src.Shares {
			link.ExpiresAt = cloneTime(link.ExpiresAt)
			link.RevokedAt = cloneTime(link.RevokedAt)
			link.LastAccessedAt = cloneTime(link.LastAccessedAt)
			out.Shares[i] = link
		}
	}
	if src.Audits != nil {
		out.Audits = make([]model.AuditEvent, len(src.Audits))
		for i, event := range src.Audits {
			event.Metadata = cloneMetadata(event.Metadata)
			out.Audits[i] = event
		}
	}
	return out
}

// mutate provides rollback semantics: changes are applied to a copy, persisted,
// and only then published to readers.
func (s *Store) mutate(apply func(*state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := cloneState(s.state)
	if err := apply(&candidate); err != nil {
		return err
	}
	if err := s.persistCandidate(candidate); err != nil {
		return err
	}
	s.state = candidate
	return nil
}

func (s *Store) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.MarshalIndent(s.state, "", "  ")
}

// WithSnapshot holds a read lock for the duration of fn. It is intended for
// consistent backup generation and deliberately blocks mutations until the
// backup has validated and copied every referenced blob.
func (s *Store) WithSnapshot(fn func(stateJSON []byte, files []model.FileAsset) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	files := append([]model.FileAsset(nil), s.state.Files...)
	return fn(data, files)
}

func (s *Store) ReplaceStateJSON(data []byte) error {
	var candidate state
	if err := json.Unmarshal(data, &candidate); err != nil {
		return err
	}
	candidate.SchemaVersion = currentStateSchema
	if candidate.Notes == nil || candidate.Files == nil || candidate.Pads == nil || candidate.Shares == nil || candidate.Audits == nil {
		return errors.New("backup state is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistCandidate(candidate); err != nil {
		return err
	}
	s.state = candidate
	return nil
}

func (s *Store) ListNotes(query, tag string) []model.QuickNote {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query = strings.ToLower(strings.TrimSpace(query))
	tag = strings.ToLower(strings.TrimSpace(tag))
	now := time.Now()
	out := make([]model.QuickNote, 0, len(s.state.Notes))
	for _, n := range s.state.Notes {
		if n.DeletedAt != nil || expired(n.ExpiresAt, now) {
			continue
		}
		if query != "" && !containsFold(n.Content, query) && !containsTag(n.Tags, query) {
			continue
		}
		if tag != "" && !containsTag(n.Tags, tag) {
			continue
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (s *Store) GetNote(id string) (model.QuickNote, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, n := range s.state.Notes {
		if n.ID == id && n.DeletedAt == nil && !expired(n.ExpiresAt, now) {
			return n, nil
		}
	}
	return model.QuickNote{}, ErrNotFound
}

func (s *Store) CreateNote(n model.QuickNote) error {
	return s.mutate(func(st *state) error {
		st.Notes = append(st.Notes, n)
		appendAudit(st, "create", "note", n.ID, "note created", nil)
		return nil
	})
}

func (s *Store) UpdateNote(id string, apply func(*model.QuickNote) error) (result model.QuickNote, err error) {
	err = s.mutate(func(st *state) error {
		now := time.Now()
		for i := range st.Notes {
			if st.Notes[i].ID != id || st.Notes[i].DeletedAt != nil || expired(st.Notes[i].ExpiresAt, now) {
				continue
			}
			if err := apply(&st.Notes[i]); err != nil {
				return err
			}
			st.Notes[i].UpdatedAt = now
			appendAudit(st, "update", "note", id, "note updated", nil)
			result = st.Notes[i]
			return nil
		}
		return ErrNotFound
	})
	return result, err
}

func (s *Store) ReadNote(id string) (result model.QuickNote, err error) {
	err = s.mutate(func(st *state) error {
		now := time.Now()
		for i := range st.Notes {
			if st.Notes[i].ID != id || st.Notes[i].DeletedAt != nil || expired(st.Notes[i].ExpiresAt, now) {
				continue
			}
			result = st.Notes[i]
			result.ReadCount++
			st.Notes[i].ReadCount = result.ReadCount
			remove := result.IsBurnAfterRead || (result.MaxReadCount > 0 && result.ReadCount >= result.MaxReadCount)
			if remove {
				st.Notes = append(st.Notes[:i], st.Notes[i+1:]...)
				revokeSharesForObject(st, "note", id, "target consumed")
			}
			appendAudit(st, "read", "note", id, "note read", map[string]interface{}{"removed": remove})
			return nil
		}
		return ErrNotFound
	})
	return result, err
}

func (s *Store) DeleteNotes(ids []string) (deleted int, err error) {
	err = s.mutate(func(st *state) error {
		set := stringSet(ids)
		now := time.Now()
		for i := range st.Notes {
			n := &st.Notes[i]
			if !set[n.ID] || n.DeletedAt != nil {
				continue
			}
			n.DeletedAt = &now
			deleted++
			appendAudit(st, "trash", "note", n.ID, "note moved to trash", nil)
		}
		if deleted == 0 {
			return ErrNotFound
		}
		return nil
	})
	return deleted, err
}

func (s *Store) ListFiles(query, kind string) []model.FileAsset {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query = strings.ToLower(strings.TrimSpace(query))
	kind = strings.ToLower(strings.TrimSpace(kind))
	now := time.Now()
	out := make([]model.FileAsset, 0, len(s.state.Files))
	for _, f := range s.state.Files {
		if f.DeletedAt != nil || expired(f.ExpiresAt, now) {
			continue
		}
		if query != "" && !containsFold(f.FileName, query) && !containsFold(f.MIMEType, query) {
			continue
		}
		if kind == "video" && !f.IsVideo {
			continue
		}
		if kind == "image" && !strings.HasPrefix(f.MIMEType, "image/") {
			continue
		}
		if kind == "document" && (f.IsVideo || strings.HasPrefix(f.MIMEType, "image/")) {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) AllFiles() []model.FileAsset {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.FileAsset(nil), s.state.Files...)
}

func (s *Store) GetFile(id string) (model.FileAsset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, f := range s.state.Files {
		if f.ID == id && f.DeletedAt == nil && !expired(f.ExpiresAt, now) {
			return f, nil
		}
	}
	return model.FileAsset{}, ErrNotFound
}

func (s *Store) GetTrashedFile(id string) (model.FileAsset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, f := range s.state.Files {
		if f.ID == id && f.DeletedAt != nil {
			return f, nil
		}
	}
	return model.FileAsset{}, ErrNotFound
}

// FindFileByHash returns only a live, physically valid object. It verifies size
// and digest so stale metadata can never cause a good re-upload to be deleted.
func (s *Store) FindFileByHash(hash string, size int64) (model.FileAsset, error) {
	s.mu.RLock()
	candidates := make([]model.FileAsset, 0)
	now := time.Now()
	for _, f := range s.state.Files {
		if f.DeletedAt == nil && !expired(f.ExpiresAt, now) && f.FileHash == hash && f.FileSize == size {
			candidates = append(candidates, f)
		}
	}
	s.mu.RUnlock()
	for _, f := range candidates {
		path := filepath.Join(s.dataDir, f.StoragePath)
		if info, err := os.Stat(path); err != nil || info.Size() != size {
			continue
		}
		actual, err := hashFile(path)
		if err == nil && subtleEqualHex(actual, hash) {
			return f, nil
		}
	}
	return model.FileAsset{}, ErrNotFound
}

func (s *Store) CreateFile(f model.FileAsset) error {
	return s.mutate(func(st *state) error {
		st.Files = append(st.Files, f)
		appendAudit(st, "upload", "file", f.ID, "file uploaded", map[string]interface{}{"name": f.FileName, "size": f.FileSize, "hash": f.FileHash})
		return nil
	})
}

func (s *Store) RenameFile(id, name string) (result model.FileAsset, err error) {
	name = sanitizeFilename(name)
	if name == "" {
		return model.FileAsset{}, errors.New("file name is required")
	}
	err = s.mutate(func(st *state) error {
		for i := range st.Files {
			if st.Files[i].ID != id || st.Files[i].DeletedAt != nil {
				continue
			}
			st.Files[i].FileName = name
			appendAudit(st, "rename", "file", id, "file renamed", map[string]interface{}{"name": name})
			result = st.Files[i]
			return nil
		}
		return ErrNotFound
	})
	return result, err
}

func (s *Store) IncrementDownload(id string) error {
	return s.mutate(func(st *state) error {
		for i := range st.Files {
			if st.Files[i].ID == id && st.Files[i].DeletedAt == nil {
				st.Files[i].DownloadCount++
				return nil
			}
		}
		return ErrNotFound
	})
}

func (s *Store) DeleteFiles(ids []string) (deleted []model.FileAsset, err error) {
	err = s.mutate(func(st *state) error {
		set := stringSet(ids)
		now := time.Now()
		for i := range st.Files {
			f := &st.Files[i]
			if !set[f.ID] || f.DeletedAt != nil {
				continue
			}
			f.DeletedAt = &now
			deleted = append(deleted, *f)
			appendAudit(st, "trash", "file", f.ID, "file moved to trash", map[string]interface{}{"name": f.FileName})
		}
		if len(deleted) == 0 {
			return ErrNotFound
		}
		return nil
	})
	return deleted, err
}

func (s *Store) StorageReferencedByOthers(storagePath, excludingID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, f := range s.state.Files {
		if f.ID != excludingID && f.StoragePath == storagePath {
			return true
		}
	}
	return false
}

func (s *Store) ListPads(query string) []model.PadDocument {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]model.PadDocument, 0, len(s.state.Pads))
	for _, p := range s.state.Pads {
		if p.DeletedAt != nil {
			continue
		}
		if query != "" && !containsFold(p.Title, query) && !containsFold(p.Content, query) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (s *Store) GetPad(id string) (model.PadDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.state.Pads {
		if p.ID == id && p.DeletedAt == nil {
			return p, nil
		}
	}
	return model.PadDocument{}, ErrNotFound
}

func (s *Store) CreatePad(p model.PadDocument) error {
	return s.mutate(func(st *state) error {
		st.Pads = append(st.Pads, p)
		appendAudit(st, "create", "pad", p.ID, "document created", map[string]interface{}{"title": p.Title})
		return nil
	})
}

func (s *Store) UpdatePad(id, title, content string, expectedVersion int) (result model.PadDocument, err error) {
	err = s.mutate(func(st *state) error {
		for i := range st.Pads {
			if st.Pads[i].ID != id || st.Pads[i].DeletedAt != nil {
				continue
			}
			if expectedVersion <= 0 || expectedVersion != st.Pads[i].Version {
				return ErrVersionConflict
			}
			if strings.TrimSpace(title) != "" {
				st.Pads[i].Title = cleanText(title, 200)
			}
			st.Pads[i].Content = content
			st.Pads[i].Version++
			st.Pads[i].UpdatedAt = time.Now()
			appendAudit(st, "update", "pad", id, "document updated", map[string]interface{}{"version": st.Pads[i].Version})
			result = st.Pads[i]
			return nil
		}
		return ErrNotFound
	})
	return result, err
}

func (s *Store) DeletePad(id string) error {
	return s.mutate(func(st *state) error {
		for i := range st.Pads {
			if st.Pads[i].ID != id || st.Pads[i].DeletedAt != nil {
				continue
			}
			now := time.Now()
			st.Pads[i].DeletedAt = &now
			appendAudit(st, "trash", "pad", id, "document moved to trash", map[string]interface{}{"title": st.Pads[i].Title})
			return nil
		}
		return ErrNotFound
	})
}

func (s *Store) CreateShare(link model.ShareLink) error {
	return s.mutate(func(st *state) error {
		st.Shares = append(st.Shares, link)
		appendAudit(st, "share", link.ObjectType, link.ObjectID, "share created", map[string]interface{}{"share_id": link.ID, "max_access_count": link.MaxAccessCount})
		return nil
	})
}

func (s *Store) ListShares() []model.ShareLink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	out := append([]model.ShareLink(nil), s.state.Shares...)
	index := buildShareTargetIndex(s.state, now)
	for i := range out {
		out[i].Status = index.statusOf(out[i], now)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) GetShareByToken(token string) (model.ShareLink, error) {
	return s.LookupShareByToken(token, false)
}

// LookupShareByToken can ignore an exhausted access counter for a previously
// authorized browser session while still enforcing revocation, expiry, and
// target lifecycle.
func (s *Store) LookupShareByToken(token string, allowExhausted bool) (model.ShareLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, link := range s.state.Shares {
		if link.Token != token {
			continue
		}
		if link.RevokedAt != nil {
			return model.ShareLink{}, ErrShareRevoked
		}
		if expired(link.ExpiresAt, now) {
			return model.ShareLink{}, ErrShareExpired
		}
		if !allowExhausted && link.MaxAccessCount > 0 && link.AccessCount >= link.MaxAccessCount {
			return model.ShareLink{}, ErrShareExhausted
		}
		link.Status = shareStatus(s.state, link, now)
		if link.Status != "active" && !(allowExhausted && link.Status == "exhausted") {
			return model.ShareLink{}, ErrNotFound
		}
		return link, nil
	}
	return model.ShareLink{}, ErrNotFound
}

func (s *Store) ConsumeShare(token string) (result model.ShareLink, err error) {
	err = s.mutate(func(st *state) error {
		now := time.Now()
		for i := range st.Shares {
			link := &st.Shares[i]
			if link.Token != token {
				continue
			}
			if err := validateShare(*link, now); err != nil {
				return err
			}
			if shareStatus(*st, *link, now) != "active" {
				return ErrNotFound
			}
			link.AccessCount++
			link.LastAccessedAt = &now
			appendAudit(st, "access", "share", link.ID, "share accessed", map[string]interface{}{"count": link.AccessCount})
			result = *link
			return nil
		}
		return ErrNotFound
	})
	return result, err
}

func (s *Store) RevokeShare(id string) error {
	return s.mutate(func(st *state) error {
		for i := range st.Shares {
			if st.Shares[i].ID != id {
				continue
			}
			if st.Shares[i].RevokedAt == nil {
				now := time.Now()
				st.Shares[i].RevokedAt = &now
				appendAudit(st, "revoke", "share", id, "share revoked", nil)
			}
			return nil
		}
		return ErrNotFound
	})
}

func (s *Store) ListTrash(kind string) []model.TrashItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	kind = strings.ToLower(strings.TrimSpace(kind))
	out := []model.TrashItem{}
	if kind == "" || kind == "note" {
		for _, n := range s.state.Notes {
			if n.DeletedAt != nil {
				out = append(out, model.TrashItem{ObjectType: "note", ID: n.ID, Name: compact(n.Content, 80), DeletedAt: *n.DeletedAt, CreatedAt: n.CreatedAt})
			}
		}
	}
	if kind == "" || kind == "file" {
		for _, f := range s.state.Files {
			if f.DeletedAt != nil {
				out = append(out, model.TrashItem{ObjectType: "file", ID: f.ID, Name: f.FileName, Size: f.FileSize, DeletedAt: *f.DeletedAt, CreatedAt: f.CreatedAt})
			}
		}
	}
	if kind == "" || kind == "pad" {
		for _, p := range s.state.Pads {
			if p.DeletedAt != nil {
				out = append(out, model.TrashItem{ObjectType: "pad", ID: p.ID, Name: p.Title, DeletedAt: *p.DeletedAt, CreatedAt: p.CreatedAt})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out
}

func (s *Store) RestoreTrash(kind, id string) error {
	return s.mutate(func(st *state) error {
		kind = strings.ToLower(strings.TrimSpace(kind))
		switch kind {
		case "note":
			for i := range st.Notes {
				if st.Notes[i].ID == id && st.Notes[i].DeletedAt != nil {
					st.Notes[i].DeletedAt = nil
					appendAudit(st, "restore", "note", id, "note restored", nil)
					return nil
				}
			}
		case "file":
			for i := range st.Files {
				if st.Files[i].ID == id && st.Files[i].DeletedAt != nil {
					if _, err := os.Stat(filepath.Join(s.dataDir, st.Files[i].StoragePath)); err != nil {
						return fmt.Errorf("file entity is missing: %w", err)
					}
					st.Files[i].DeletedAt = nil
					appendAudit(st, "restore", "file", id, "file restored", map[string]interface{}{"name": st.Files[i].FileName})
					return nil
				}
			}
		case "pad":
			for i := range st.Pads {
				if st.Pads[i].ID == id && st.Pads[i].DeletedAt != nil {
					st.Pads[i].DeletedAt = nil
					appendAudit(st, "restore", "pad", id, "document restored", nil)
					return nil
				}
			}
		default:
			return errors.New("unsupported trash type")
		}
		return ErrNotFound
	})
}

func (s *Store) PurgeTrash(kind, id string) (removed *model.FileAsset, err error) {
	err = s.mutate(func(st *state) error {
		kind = strings.ToLower(strings.TrimSpace(kind))
		switch kind {
		case "note":
			for i, n := range st.Notes {
				if n.ID == id && n.DeletedAt != nil {
					st.Notes = append(st.Notes[:i], st.Notes[i+1:]...)
					revokeSharesForObject(st, "note", id, "target purged")
					appendAudit(st, "purge", "note", id, "note permanently deleted", nil)
					return nil
				}
			}
		case "file":
			for i, f := range st.Files {
				if f.ID == id && f.DeletedAt != nil {
					copy := f
					removed = &copy
					st.Files = append(st.Files[:i], st.Files[i+1:]...)
					revokeSharesForObject(st, "file", id, "target purged")
					appendAudit(st, "purge", "file", id, "file permanently deleted", map[string]interface{}{"name": f.FileName})
					return nil
				}
			}
		case "pad":
			for i, p := range st.Pads {
				if p.ID == id && p.DeletedAt != nil {
					st.Pads = append(st.Pads[:i], st.Pads[i+1:]...)
					revokeSharesForObject(st, "pad", id, "target purged")
					appendAudit(st, "purge", "pad", id, "document permanently deleted", nil)
					return nil
				}
			}
		default:
			return errors.New("unsupported trash type")
		}
		return ErrNotFound
	})
	return removed, err
}

func (s *Store) EmptyTrash() (files []model.FileAsset, deletedCount int, err error) {
	err = s.mutate(func(st *state) error {
		purgedNotes := map[string]struct{}{}
		purgedFiles := map[string]struct{}{}
		purgedPads := map[string]struct{}{}
		notes := st.Notes[:0]
		for _, n := range st.Notes {
			if n.DeletedAt != nil {
				deletedCount++
				purgedNotes[n.ID] = struct{}{}
				continue
			}
			notes = append(notes, n)
		}
		st.Notes = notes
		activeFiles := st.Files[:0]
		for _, f := range st.Files {
			if f.DeletedAt != nil {
				deletedCount++
				files = append(files, f)
				purgedFiles[f.ID] = struct{}{}
				continue
			}
			activeFiles = append(activeFiles, f)
		}
		st.Files = activeFiles
		pads := st.Pads[:0]
		for _, p := range st.Pads {
			if p.DeletedAt != nil {
				deletedCount++
				purgedPads[p.ID] = struct{}{}
				continue
			}
			pads = append(pads, p)
		}
		st.Pads = pads
		if deletedCount == 0 {
			return ErrNotFound
		}
		// One pass per object kind keeps this linear in shares instead of
		// rescanning every share once per purged object.
		revokeSharesForObjects(st, "note", purgedNotes, "target purged")
		revokeSharesForObjects(st, "file", purgedFiles, "target purged")
		revokeSharesForObjects(st, "pad", purgedPads, "target purged")
		appendAudit(st, "purge", "trash", "", "trash emptied", map[string]interface{}{"count": deletedCount, "files": len(files)})
		return nil
	})
	return files, deletedCount, err
}

func (s *Store) ListAudits(limit int) []model.AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	start := len(s.state.Audits) - limit
	if start < 0 {
		start = 0
	}
	slice := s.state.Audits[start:]
	out := make([]model.AuditEvent, len(slice))
	for i, e := range slice {
		out[len(slice)-1-i] = e
	}
	return out
}

func (s *Store) AddAudit(action, object, objectID, summary string, metadata map[string]interface{}) error {
	return s.mutate(func(st *state) error {
		appendAudit(st, action, object, objectID, summary, metadata)
		return nil
	})
}

func (s *Store) Stats() model.DashboardStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	st := model.DashboardStats{}
	blobSizes := map[string]int64{}
	index := shareTargetIndex{
		notes: make(map[string]string, len(s.state.Notes)),
		files: make(map[string]string, len(s.state.Files)),
		pads:  make(map[string]string, len(s.state.Pads)),
	}
	for _, n := range s.state.Notes {
		switch {
		case n.DeletedAt != nil:
			st.TrashCount++
			index.notes[n.ID] = "suspended"
		case expired(n.ExpiresAt, now):
			index.notes[n.ID] = "target_expired"
		default:
			st.NotesCount++
			index.notes[n.ID] = "active"
		}
	}
	for _, f := range s.state.Files {
		switch {
		case f.DeletedAt != nil:
			st.TrashCount++
			index.files[f.ID] = "suspended"
			continue
		case expired(f.ExpiresAt, now):
			index.files[f.ID] = "target_expired"
			continue
		}
		st.FilesCount++
		blobSizes[f.StoragePath] = f.FileSize
		index.files[f.ID] = "active"
		if f.IsVideo {
			st.VideosCount++
		}
	}
	for _, size := range blobSizes {
		st.StorageUsed += size
	}
	for _, p := range s.state.Pads {
		if p.DeletedAt != nil {
			st.TrashCount++
			index.pads[p.ID] = "suspended"
		} else {
			st.PadsCount++
			index.pads[p.ID] = "active"
		}
	}
	for _, link := range s.state.Shares {
		if index.statusOf(link, now) == "active" {
			st.ActiveShares++
		}
	}
	return st
}

func (s *Store) CleanupExpired(retentionDays int) (moved int, err error) {
	// Cheap read-only precheck: the cleanup worker runs every minute, and cloning
	// plus serializing a large idle state just to discover nothing changed is a
	// significant background CPU and allocation cost.
	probe := time.Now()
	probeCutoff := time.Time{}
	if retentionDays > 0 {
		probeCutoff = probe.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	}
	s.mu.RLock()
	pending := false
	for i := range s.state.Notes {
		n := &s.state.Notes[i]
		if n.DeletedAt != nil {
			continue
		}
		if expired(n.ExpiresAt, probe) || (!probeCutoff.IsZero() && n.UpdatedAt.Before(probeCutoff) && !n.Pinned) {
			pending = true
			break
		}
	}
	if !pending {
		for i := range s.state.Files {
			f := &s.state.Files[i]
			if f.DeletedAt != nil {
				continue
			}
			if expired(f.ExpiresAt, probe) || (!probeCutoff.IsZero() && f.CreatedAt.Before(probeCutoff)) {
				pending = true
				break
			}
		}
	}
	if !pending && !probeCutoff.IsZero() {
		for i := range s.state.Pads {
			p := &s.state.Pads[i]
			if p.DeletedAt == nil && p.UpdatedAt.Before(probeCutoff) {
				pending = true
				break
			}
		}
	}
	s.mu.RUnlock()
	if !pending {
		return 0, nil
	}

	err = s.mutate(func(st *state) error {
		now := time.Now()
		cutoff := time.Time{}
		if retentionDays > 0 {
			cutoff = now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
		}
		for i := range st.Notes {
			n := &st.Notes[i]
			if n.DeletedAt != nil {
				continue
			}
			if expired(n.ExpiresAt, now) || (!cutoff.IsZero() && n.UpdatedAt.Before(cutoff) && !n.Pinned) {
				n.DeletedAt = &now
				moved++
			}
		}
		for i := range st.Files {
			f := &st.Files[i]
			if f.DeletedAt != nil {
				continue
			}
			if expired(f.ExpiresAt, now) || (!cutoff.IsZero() && f.CreatedAt.Before(cutoff)) {
				f.DeletedAt = &now
				moved++
			}
		}
		for i := range st.Pads {
			p := &st.Pads[i]
			if p.DeletedAt == nil && !cutoff.IsZero() && p.UpdatedAt.Before(cutoff) {
				p.DeletedAt = &now
				moved++
			}
		}
		if moved == 0 {
			return errNoChange
		}
		appendAudit(st, "cleanup", "system", "", "expired items moved to trash", map[string]interface{}{"count": moved})
		return nil
	})
	if errors.Is(err, errNoChange) {
		return 0, nil
	}
	return moved, err
}

func (s *Store) PurgeExpiredTrash(days int) (files []model.FileAsset, purged int, err error) {
	if days <= 0 {
		return nil, 0, nil
	}
	// Read-only precheck to keep the minute-interval cleanup worker from
	// cloning and rewriting the whole dataset when there is nothing to purge.
	probeNow := time.Now()
	probeCutoff := probeNow.Add(-time.Duration(days) * 24 * time.Hour)
	probeShareCutoff := probeNow.Add(-30 * 24 * time.Hour)
	s.mu.RLock()
	pending := false
	for i := range s.state.Notes {
		if n := &s.state.Notes[i]; n.DeletedAt != nil && n.DeletedAt.Before(probeCutoff) {
			pending = true
			break
		}
	}
	if !pending {
		for i := range s.state.Files {
			if f := &s.state.Files[i]; f.DeletedAt != nil && f.DeletedAt.Before(probeCutoff) {
				pending = true
				break
			}
		}
	}
	if !pending {
		for i := range s.state.Pads {
			if p := &s.state.Pads[i]; p.DeletedAt != nil && p.DeletedAt.Before(probeCutoff) {
				pending = true
				break
			}
		}
	}
	if !pending {
		for _, link := range s.state.Shares {
			terminal := link.RevokedAt != nil || expired(link.ExpiresAt, probeNow) || (link.MaxAccessCount > 0 && link.AccessCount >= link.MaxAccessCount)
			if terminal && link.CreatedAt.Before(probeShareCutoff) {
				pending = true
				break
			}
		}
	}
	s.mu.RUnlock()
	if !pending {
		return nil, 0, nil
	}

	err = s.mutate(func(st *state) error {
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		notes := st.Notes[:0]
		purgedNotes := map[string]struct{}{}
		purgedFiles := map[string]struct{}{}
		purgedPads := map[string]struct{}{}
		for _, n := range st.Notes {
			if n.DeletedAt != nil && n.DeletedAt.Before(cutoff) {
				purged++
				purgedNotes[n.ID] = struct{}{}
				continue
			}
			notes = append(notes, n)
		}
		st.Notes = notes
		activeFiles := st.Files[:0]
		for _, f := range st.Files {
			if f.DeletedAt != nil && f.DeletedAt.Before(cutoff) {
				files = append(files, f)
				purged++
				purgedFiles[f.ID] = struct{}{}
				continue
			}
			activeFiles = append(activeFiles, f)
		}
		st.Files = activeFiles
		pads := st.Pads[:0]
		for _, p := range st.Pads {
			if p.DeletedAt != nil && p.DeletedAt.Before(cutoff) {
				purged++
				purgedPads[p.ID] = struct{}{}
				continue
			}
			pads = append(pads, p)
		}
		st.Pads = pads
		revokeSharesForObjects(st, "note", purgedNotes, "expired trash purged")
		revokeSharesForObjects(st, "file", purgedFiles, "expired trash purged")
		revokeSharesForObjects(st, "pad", purgedPads, "expired trash purged")
		// Remove terminal shares after 30 days to prevent unbounded metadata.
		originalShareCount := len(st.Shares)
		shares := st.Shares[:0]
		shareCutoff := time.Now().Add(-30 * 24 * time.Hour)
		for _, link := range st.Shares {
			terminal := link.RevokedAt != nil || expired(link.ExpiresAt, time.Now()) || (link.MaxAccessCount > 0 && link.AccessCount >= link.MaxAccessCount)
			if terminal && link.CreatedAt.Before(shareCutoff) {
				continue
			}
			shares = append(shares, link)
		}
		st.Shares = shares
		if purged == 0 && len(shares) == originalShareCount {
			return errNoChange
		}
		appendAudit(st, "purge", "trash", "", "expired trash purged", map[string]interface{}{"count": purged, "files": len(files)})
		return nil
	})
	if errors.Is(err, errNoChange) {
		return nil, 0, nil
	}
	return files, purged, err
}

var errNoChange = errors.New("no state change")

func validateShare(link model.ShareLink, now time.Time) error {
	if link.RevokedAt != nil {
		return ErrShareRevoked
	}
	if expired(link.ExpiresAt, now) {
		return ErrShareExpired
	}
	if link.MaxAccessCount > 0 && link.AccessCount >= link.MaxAccessCount {
		return ErrShareExhausted
	}
	return nil
}

func shareStatus(st state, link model.ShareLink, now time.Time) string {
	if link.RevokedAt != nil {
		return "revoked"
	}
	if expired(link.ExpiresAt, now) {
		return "expired"
	}
	if link.MaxAccessCount > 0 && link.AccessCount >= link.MaxAccessCount {
		return "exhausted"
	}
	switch link.ObjectType {
	case "note":
		for _, n := range st.Notes {
			if n.ID == link.ObjectID {
				if n.DeletedAt != nil {
					return "suspended"
				}
				if expired(n.ExpiresAt, now) {
					return "target_expired"
				}
				return "active"
			}
		}
	case "file":
		for _, f := range st.Files {
			if f.ID == link.ObjectID {
				if f.DeletedAt != nil {
					return "suspended"
				}
				if expired(f.ExpiresAt, now) {
					return "target_expired"
				}
				return "active"
			}
		}
	case "pad":
		for _, p := range st.Pads {
			if p.ID == link.ObjectID {
				if p.DeletedAt != nil {
					return "suspended"
				}
				return "active"
			}
		}
	}
	return "target_missing"
}

// shareTargetIndex maps every target object ID to its lifecycle state once, so
// listing or counting N shares costs O(N + objects) instead of O(N * objects).
type shareTargetIndex struct {
	notes map[string]string
	files map[string]string
	pads  map[string]string
}

func (index shareTargetIndex) statusOf(link model.ShareLink, now time.Time) string {
	if link.RevokedAt != nil {
		return "revoked"
	}
	if expired(link.ExpiresAt, now) {
		return "expired"
	}
	if link.MaxAccessCount > 0 && link.AccessCount >= link.MaxAccessCount {
		return "exhausted"
	}
	switch link.ObjectType {
	case "note":
		if status, ok := index.notes[link.ObjectID]; ok {
			return status
		}
	case "file":
		if status, ok := index.files[link.ObjectID]; ok {
			return status
		}
	case "pad":
		if status, ok := index.pads[link.ObjectID]; ok {
			return status
		}
	}
	return "target_missing"
}

func buildShareTargetIndex(st state, now time.Time) shareTargetIndex {
	index := shareTargetIndex{
		notes: make(map[string]string, len(st.Notes)),
		files: make(map[string]string, len(st.Files)),
		pads:  make(map[string]string, len(st.Pads)),
	}
	for _, n := range st.Notes {
		switch {
		case n.DeletedAt != nil:
			index.notes[n.ID] = "suspended"
		case expired(n.ExpiresAt, now):
			index.notes[n.ID] = "target_expired"
		default:
			index.notes[n.ID] = "active"
		}
	}
	for _, f := range st.Files {
		switch {
		case f.DeletedAt != nil:
			index.files[f.ID] = "suspended"
		case expired(f.ExpiresAt, now):
			index.files[f.ID] = "target_expired"
		default:
			index.files[f.ID] = "active"
		}
	}
	for _, p := range st.Pads {
		if p.DeletedAt != nil {
			index.pads[p.ID] = "suspended"
		} else {
			index.pads[p.ID] = "active"
		}
	}
	return index
}

func revokeSharesForObject(st *state, kind, id, reason string) {
	now := time.Now()
	for i := range st.Shares {
		if st.Shares[i].ObjectType == kind && st.Shares[i].ObjectID == id && st.Shares[i].RevokedAt == nil {
			st.Shares[i].RevokedAt = &now
			appendAudit(st, "revoke", "share", st.Shares[i].ID, "share automatically revoked", map[string]interface{}{"reason": reason, "object_type": kind, "object_id": id})
		}
	}
}

// revokeSharesForObjects revokes every share pointing at one of ids in a single
// pass over the share list.
func revokeSharesForObjects(st *state, kind string, ids map[string]struct{}, reason string) {
	if len(ids) == 0 {
		return
	}
	now := time.Now()
	for i := range st.Shares {
		link := &st.Shares[i]
		if link.ObjectType != kind || link.RevokedAt != nil {
			continue
		}
		if _, ok := ids[link.ObjectID]; !ok {
			continue
		}
		link.RevokedAt = &now
		appendAudit(st, "revoke", "share", link.ID, "share automatically revoked", map[string]interface{}{"reason": reason, "object_type": kind, "object_id": link.ObjectID})
	}
}

func appendAudit(st *state, action, object, objectID, summary string, metadata map[string]interface{}) {
	prev := ""
	if n := len(st.Audits); n > 0 {
		prev = st.Audits[n-1].Hash
	}
	e := model.AuditEvent{ID: newID("evt"), Action: action, Object: object, ObjectID: objectID, Summary: summary, Metadata: metadata, PrevHash: prev, CreatedAt: time.Now().UTC()}
	e.Hash = auditHash(e)
	st.Audits = append(st.Audits, e)
}

func (s *Store) rebuildAuditChainLocked() {
	prev := ""
	for i := range s.state.Audits {
		s.state.Audits[i].PrevHash = prev
		s.state.Audits[i].Hash = auditHash(s.state.Audits[i])
		prev = s.state.Audits[i].Hash
	}
}

func auditHash(e model.AuditEvent) string {
	payload := struct {
		ID, Action, Object, ObjectID, Summary, PrevHash string
		Metadata                                        map[string]interface{}
		CreatedAt                                       time.Time
	}{e.ID, e.Action, e.Object, e.ObjectID, e.Summary, e.PrevHash, e.Metadata, e.CreatedAt}
	data, _ := json.Marshal(payload)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func NewID(prefix string) string { return newID(prefix) }

var fallbackIDCounter atomic.Uint64

func newID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UTC().UnixMilli(), hex.EncodeToString(buf))
	}
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UTC().UnixNano(), fallbackIDCounter.Add(1))
}

func NewSecureToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("secure random source unavailable: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// NewToken is retained for source compatibility in tests. Production paths use
// NewSecureToken and fail closed on entropy errors.
func NewToken() string {
	token, err := NewSecureToken()
	if err != nil {
		panic(err)
	}
	return token
}

func stringSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = true
		}
	}
	return set
}

func containsFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	if isASCII(needle) {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if asciiEqualFold(haystack[i:i+len(needle)], needle) {
				return true
			}
		}
		return false
	}
	return strings.Contains(strings.ToLower(haystack), needle)
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

// asciiEqualFold compares a haystack window against an already lower-cased ASCII
// needle. ASCII bytes never appear inside multi-byte UTF-8 sequences, so a
// byte-wise search is safe for mixed Chinese/ASCII content.
func asciiEqualFold(window, needle string) bool {
	for i := 0; i < len(needle); i++ {
		c := window[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != needle[i] {
			return false
		}
	}
	return true
}

func containsTag(tags []string, target string) bool {
	target = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(target), "#"))
	for _, t := range tags {
		if strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t), "#")) == target {
			return true
		}
	}
	return false
}

func compact(value string, n int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) <= n {
		return value
	}
	r := []rune(value)
	return string(r[:n]) + "…"
}

func expired(at *time.Time, now time.Time) bool {
	return at != nil && !at.After(now)
}

func sanitizeFilename(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	if value == "." || value == string(filepath.Separator) {
		return ""
	}
	return cleanText(value, 240)
}

func cleanText(value string, max int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\u202a' || r == '\u202b' || r == '\u202d' || r == '\u202e' || r == '\u2066' || r == '\u2067' || r == '\u2068' || r == '\u2069' {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	r := []rune(value)
	if len(r) > max {
		value = string(r[:max])
	}
	return value
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func subtleEqualHex(a, b string) bool {
	ab, errA := hex.DecodeString(a)
	bb, errB := hex.DecodeString(b)
	if errA != nil || errB != nil || len(ab) != len(bb) {
		return false
	}
	var diff byte
	for i := range ab {
		diff |= ab[i] ^ bb[i]
	}
	return diff == 0
}

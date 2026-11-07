package storage

import "omnishare/internal/model"

// Backend is the storage contract shared by the SQLite engine (production) and
// the JSON engine (legacy fallback). The API layer talks to this interface so
// either engine can serve requests without type switches at every call site.
type Backend interface {
	DataDir() string

	ListNotes(query, tag string) []model.QuickNote
	GetNote(id string) (model.QuickNote, error)
	CreateNote(n model.QuickNote) error
	UpdateNote(id string, apply func(*model.QuickNote) error) (model.QuickNote, error)
	ReadNote(id string) (model.QuickNote, error)
	DeleteNotes(ids []string) (int, error)

	ListFiles(query, kind string) []model.FileAsset
	AllFiles() []model.FileAsset
	GetFile(id string) (model.FileAsset, error)
	GetTrashedFile(id string) (model.FileAsset, error)
	FindFileByHash(hash string, size int64) (model.FileAsset, error)
	CreateFile(f model.FileAsset) error
	RenameFile(id, name string) (model.FileAsset, error)
	IncrementDownload(id string) error
	DeleteFiles(ids []string) ([]model.FileAsset, error)
	StorageReferencedByOthers(storagePath, excludingID string) bool

	ListPads(query string) []model.PadDocument
	GetPad(id string) (model.PadDocument, error)
	CreatePad(p model.PadDocument) error
	UpdatePad(id, title, content string, expectedVersion int) (model.PadDocument, error)
	DeletePad(id string) error

	CreateShare(link model.ShareLink) error
	ListShares() []model.ShareLink
	GetShareByToken(token string) (model.ShareLink, error)
	LookupShareByToken(token string, allowExhausted bool) (model.ShareLink, error)
	ConsumeShare(token string) (model.ShareLink, error)
	RevokeShare(id string) error

	ListTrash(kind string) []model.TrashItem
	RestoreTrash(kind, id string) error
	PurgeTrash(kind, id string) (*model.FileAsset, error)
	EmptyTrash() ([]model.FileAsset, int, error)

	ListAudits(limit int) []model.AuditEvent
	AddAudit(action, object, objectID, summary string, metadata map[string]interface{}) error
	Stats() model.DashboardStats
	CleanupExpired(retentionDays int) (int, error)
	PurgeExpiredTrash(days int) ([]model.FileAsset, int, error)

	Snapshot() ([]byte, error)
	WithSnapshot(fn func(stateJSON []byte, files []model.FileAsset) error) error
	ReplaceStateJSON(data []byte) error

	// FlushStats writes buffered read/download counters. Read paths never write
	// synchronously; the maintenance loop and shutdown flush them in batches.
	FlushStats()
	// PruneAudits applies the audit retention policy. Implementations must
	// preserve verifiability; the JSON fallback is a no-op because it has no
	// anchor table.
	PruneAudits(retentionDays, maxEvents int) (int, error)
	Close() error
}

package storage

import (
	"fmt"
	"testing"
	"time"

	"omnishare/internal/model"
)

// buildLargeState constructs a realistic large dataset without going through the
// public write API (which would be quadratic for setup).
func buildLargeState(tb testing.TB, notes, files, pads, shares, audits int) *Store {
	tb.Helper()
	s, err := New(tb.TempDir())
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	st := state{SchemaVersion: currentStateSchema}
	for i := 0; i < notes; i++ {
		st.Notes = append(st.Notes, model.QuickNote{
			ID:        fmt.Sprintf("n_%06d", i),
			Content:   fmt.Sprintf("benchmark note content %d #tag%d", i, i%17),
			Tags:      []string{fmt.Sprintf("tag%d", i%17)},
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	for i := 0; i < files; i++ {
		st.Files = append(st.Files, model.FileAsset{
			ID:          fmt.Sprintf("f_%06d", i),
			FileName:    fmt.Sprintf("file_%d.bin", i),
			FileSize:    int64(1024 * (i%64 + 1)),
			MIMEType:    "application/octet-stream",
			StoragePath: fmt.Sprintf("uploads/blobs/blob_%d", i),
			FileHash:    fmt.Sprintf("%064d", i),
			CreatedAt:   now,
		})
	}
	for i := 0; i < pads; i++ {
		st.Pads = append(st.Pads, model.PadDocument{
			ID:        fmt.Sprintf("pad_%06d", i),
			Title:     fmt.Sprintf("pad %d", i),
			Content:   "content",
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	for i := 0; i < shares; i++ {
		objectType := "note"
		objectID := fmt.Sprintf("n_%06d", i%notes)
		if i%3 == 1 {
			objectType = "file"
			objectID = fmt.Sprintf("f_%06d", i%files)
		} else if i%3 == 2 {
			objectType = "pad"
			objectID = fmt.Sprintf("pad_%06d", i%pads)
		}
		st.Shares = append(st.Shares, model.ShareLink{
			ID:         fmt.Sprintf("shr_%06d", i),
			Token:      fmt.Sprintf("token-%06d", i),
			ObjectType: objectType,
			ObjectID:   objectID,
			Name:       "share",
			CreatedAt:  now,
		})
	}
	for i := 0; i < audits; i++ {
		evt := model.AuditEvent{
			ID:        fmt.Sprintf("evt_%06d", i),
			Action:    "create",
			Object:    "note",
			ObjectID:  fmt.Sprintf("n_%06d", i%notes),
			Summary:   "benchmark audit",
			CreatedAt: now,
		}
		evt.Hash = auditHash(evt)
		st.Audits = append(st.Audits, evt)
	}
	s.mu.Lock()
	s.state = st
	s.initializeState()
	s.mu.Unlock()
	if err := s.persistCandidate(s.state); err != nil {
		tb.Fatalf("persist baseline: %v", err)
	}
	return s
}

func BenchmarkStoreCreateNoteLargeState(b *testing.B) {
	s := buildLargeState(b, 20000, 1000, 2000, 500, 2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now := time.Now()
		if err := s.CreateNote(model.QuickNote{
			ID:        fmt.Sprintf("n_new_%d", i),
			Content:   "new note",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			b.Fatalf("CreateNote: %v", err)
		}
	}
}

func BenchmarkStoreStatsLargeState(b *testing.B) {
	s := buildLargeState(b, 5000, 1000, 500, 3000, 500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Stats()
	}
}

func BenchmarkStoreCleanupNoop(b *testing.B) {
	s := buildLargeState(b, 20000, 1000, 2000, 500, 2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.CleanupExpired(0); err != nil {
			b.Fatalf("CleanupExpired: %v", err)
		}
		if _, _, err := s.PurgeExpiredTrash(30); err != nil {
			b.Fatalf("PurgeExpiredTrash: %v", err)
		}
	}
}

func BenchmarkStoreIncrementDownload(b *testing.B) {
	s := buildLargeState(b, 20000, 1000, 2000, 500, 2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.IncrementDownload(fmt.Sprintf("f_%06d", i%1000)); err != nil {
			b.Fatalf("IncrementDownload: %v", err)
		}
	}
}

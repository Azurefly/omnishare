package storage

import (
	"errors"
	"sync"
	"testing"
	"time"

	"omnishare/internal/model"
)

func TestSQLiteLimitedNoteCountsEveryRead(t *testing.T) {
	store := counterRecoveryStore(t)
	now := time.Now().UTC()
	if err := store.CreateNote(model.QuickNote{ID: "limited", Content: "secret", MaxReadCount: 2, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 2; want++ {
		note, err := store.ReadNote("limited")
		if err != nil {
			t.Fatal(err)
		}
		if note.ReadCount != want {
			t.Fatalf("read count = %d, want %d", note.ReadCount, want)
		}
	}
	if _, err := store.ReadNote("limited"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read after limit = %v, want ErrNotFound", err)
	}
}

func TestSQLiteConcurrentNoteReadsRespectLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		burn bool
		max  int
		want int
	}{
		{name: "burn", burn: true, want: 1},
		{name: "limited", max: 3, want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := counterRecoveryStore(t)
			now := time.Now().UTC()
			if err := store.CreateNote(model.QuickNote{ID: "secret", Content: "secret", IsBurnAfterRead: tc.burn, MaxReadCount: tc.max, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			const readers = 32
			start := make(chan struct{})
			results := make(chan error, readers)
			var ready sync.WaitGroup
			ready.Add(readers)
			for i := 0; i < readers; i++ {
				go func() {
					ready.Done()
					<-start
					_, err := store.ReadNote("secret")
					results <- err
				}()
			}
			ready.Wait()
			close(start)
			succeeded := 0
			for i := 0; i < readers; i++ {
				err := <-results
				if err == nil {
					succeeded++
				} else if !errors.Is(err, ErrNotFound) {
					t.Errorf("read failed: %v", err)
				}
			}
			if succeeded != tc.want {
				t.Fatalf("successful reads = %d, want %d", succeeded, tc.want)
			}
		})
	}
}

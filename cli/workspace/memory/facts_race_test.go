package memory

import (
	"fmt"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// TestFactIndexConcurrentReadsAreRaceFree pins read-path thread safety under
// the race detector. Concurrent retrieval is reachable in production: MoA runs
// participants in parallel with @memory recall granted, while the background
// extraction worker searches the same index. The old recalcScoresLocked wrote
// f.Score while holding only the READ lock, so two concurrent readers raced
// on every fact's Score field.
func TestFactIndexConcurrentReadsAreRaceFree(t *testing.T) {
	dir := t.TempDir()
	fi := NewFactIndex(dir, DefaultConfig(), zap.NewNop())
	for i := 0; i < 50; i++ {
		fi.AddFact(fmt.Sprintf("service%d listens on port %d and logs to /var/log/s%d", i, 4000+i, i), "general", nil)
	}

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				switch (w + j) % 4 {
				case 0:
					fi.Search([]string{"service", "port"})
				case 1:
					fi.GetAll()
				case 2:
					fi.SearchBlended([]string{"logs"}, nil, DefaultRankWeights())
				case 3:
					fi.GetByCategory("general")
				}
			}
		}(w)
	}
	wg.Wait()
}

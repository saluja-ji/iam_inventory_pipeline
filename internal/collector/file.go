package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/saluja-ji/iam-inventory/internal/model"
)

// FileSource replays a previously saved snapshot, so the whole pipeline can run offline.
type FileSource struct{ Path string }

func (f FileSource) Collect(_ context.Context, runID string, now time.Time) (*model.Snapshot, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	var s model.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode %s: %w", f.Path, err)
	}
	s.RunID, s.CollectedAt = runID, now.UTC()
	return &s, nil
}

package fault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry records one live fault on disk, so an agent that restarts reverts what its
// predecessor injected before it does anything else.
type Entry struct {
	Namespace     string    `json:"namespace"`
	Experiment    string    `json:"experiment"`
	ExperimentUID string    `json:"experimentUID"`
	Pod           string    `json:"pod"`
	PodUID        string    `json:"podUID"`
	Kind          string    `json:"kind"`
	NetnsPID      int       `json:"netnsPID,omitempty"`
	BurnerPID     int       `json:"burnerPID,omitempty"`
	Deadline      time.Time `json:"deadline"`
	AppliedAt     time.Time `json:"appliedAt"`
}

func (e Entry) Key() string {
	return e.ExperimentUID + "_" + e.PodUID
}

type Ledger struct {
	Dir string
}

func (l Ledger) path(key string) string {
	return filepath.Join(l.Dir, strings.ReplaceAll(key, "/", "_")+".json")
}

func (l Ledger) Put(e Entry) error {
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp := l.path(e.Key()) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path(e.Key()))
}

func (l Ledger) Get(key string) (Entry, bool, error) {
	b, err := os.ReadFile(l.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return Entry{}, false, fmt.Errorf("ledger entry %s: %w", key, err)
	}
	return e, true, nil
}

func (l Ledger) Delete(key string) error {
	if err := os.Remove(l.path(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l Ledger) List() ([]Entry, error) {
	entries, err := os.ReadDir(l.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, f := range entries {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		e, ok, err := l.Get(strings.TrimSuffix(f.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, e)
		}
	}
	return out, nil
}

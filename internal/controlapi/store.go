package controlapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Dir() string { return s.dir }

func (s *Store) Ensure() error {
	for _, dir := range []string{s.dir, filepath.Join(s.dir, "sources"), filepath.Join(s.dir, "operations")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Recovery() (RecoveryState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state RecoveryState
	err := readJSON(filepath.Join(s.dir, "recovery.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return RecoveryState{SchemaVersion: SchemaVersion, Stage: RecoveryIdle}, nil
	}
	return state, err
}

func (s *Store) SaveRecovery(state RecoveryState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveRecoveryLocked(state)
}

func (s *Store) saveRecoveryLocked(state RecoveryState) error {
	state.SchemaVersion = SchemaVersion
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "recovery.json"), append(data, '\n'), 0o600)
}

func (s *Store) SaveOperation(op Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "operations", op.ID+".json"), append(data, '\n'), 0o600)
}

func (s *Store) Operation(id string) (Operation, error) {
	var op Operation
	if id == "" || filepath.Base(id) != id {
		return op, fmt.Errorf("invalid operation id")
	}
	err := readJSON(filepath.Join(s.dir, "operations", id+".json"), &op)
	return op, err
}

func (s *Store) Operations(limit int) ([]Operation, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "operations"))
	if err != nil {
		return nil, err
	}
	operations := make([]Operation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var operation Operation
		if err := readJSON(filepath.Join(s.dir, "operations", entry.Name()), &operation); err == nil {
			operations = append(operations, operation)
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].UpdatedAt.After(operations[j].UpdatedAt) })
	if limit > 0 && len(operations) > limit {
		operations = operations[:limit]
	}
	return operations, nil
}

func (s *Store) Sources() ([]Source, error) {
	var sources []Source
	err := readJSON(filepath.Join(s.dir, "sources.json"), &sources)
	if errors.Is(err, os.ErrNotExist) {
		return []Source{}, nil
	}
	return sources, err
}

func (s *Store) SaveSources(sources []Source) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(sources, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "sources.json"), append(data, '\n'), 0o600)
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".opensurge-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	if err := preserveReplacedGroup(path, tmpPath); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// preserveReplacedGroup carries the group of the file being replaced onto its
// successor. Group ownership is load-bearing here: /etc/opensurge/config.yaml is
// root:opensurge so the unprivileged control service can read it, and a rewrite
// that silently produced root:root would stop the gateway from starting.
//
// The packaged directories are setgid, which already gives the temporary file the
// right group. This is the fallback for a host whose directories lost that bit,
// and it is deliberately best-effort: the gateway helper runs without CAP_CHOWN,
// so chown there fails with EPERM and the setgid inheritance is what applies.
func preserveReplacedGroup(path, tmpPath string) error {
	info, err := os.Stat(path)
	if err != nil {
		// Nothing is being replaced, so there is no group to carry over.
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	tmpInfo, err := os.Stat(tmpPath)
	if err != nil {
		return err
	}
	if tmpStat, ok := tmpInfo.Sys().(*syscall.Stat_t); ok && tmpStat.Gid == stat.Gid {
		return nil
	}
	if err := os.Chown(tmpPath, -1, int(stat.Gid)); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	return nil
}

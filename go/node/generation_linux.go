package node

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

const generationStateSchema = "wow-sidecar.generation-state.v1"

type generationState struct {
	Schema     string `json:"schema"`
	NodeID     string `json:"node_id"`
	Generation uint64 `json:"generation"`
}

func allocateGeneration(path, nodeID string) (uint64, error) {
	if path == "" {
		return 0, fmt.Errorf("generation state path is required")
	}
	if nodeID == "" {
		return 0, fmt.Errorf("node id is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("create generation state directory: %w", err)
	}

	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open generation lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return 0, fmt.Errorf("lock generation state: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	current := uint64(0)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		decoder := json.NewDecoder(bytesReader(raw))
		decoder.DisallowUnknownFields()
		var state generationState
		if err := decoder.Decode(&state); err != nil {
			return 0, fmt.Errorf("decode generation state: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return 0, fmt.Errorf("generation state contains trailing data")
		}
		if state.Schema != generationStateSchema {
			return 0, fmt.Errorf("unsupported generation state schema")
		}
		if state.NodeID != nodeID {
			return 0, fmt.Errorf("generation state node identity mismatch")
		}
		current = state.Generation
	case os.IsNotExist(err):
		current = 0
	default:
		return 0, fmt.Errorf("read generation state: %w", err)
	}

	if current == ^uint64(0) {
		return 0, fmt.Errorf("generation counter exhausted")
	}
	next := current + 1
	state := generationState{
		Schema: generationStateSchema,
		NodeID: nodeID,
		Generation: next,
	}
	if err := writeGenerationStateAtomic(path, state); err != nil {
		return 0, err
	}
	return next, nil
}

func writeGenerationStateAtomic(path string, state generationState) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	temp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("create generation temp file: %w", err)
	}
	tempPath := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod generation temp file: %w", err)
	}
	encoder := json.NewEncoder(temp)
	if err := encoder.Encode(state); err != nil {
		return fmt.Errorf("encode generation state: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync generation temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close generation temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace generation state: %w", err)
	}
	keep = true

	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open generation state directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync generation state directory: %w", err)
	}
	return nil
}

type byteReader struct {
	raw []byte
	pos int
}

func bytesReader(raw []byte) *byteReader {
	return &byteReader{raw: raw}
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.raw) {
		return 0, io.EOF
	}
	n := copy(p, r.raw[r.pos:])
	r.pos += n
	return n, nil
}

func sortedUnique(values []string) ([]string, error) {
	out := append([]string(nil), values...)
	sort.Strings(out)
	for i, value := range out {
		if value == "" {
			return nil, fmt.Errorf("empty value")
		}
		if i > 0 && value == out[i-1] {
			return nil, fmt.Errorf("duplicate value")
		}
	}
	return out, nil
}

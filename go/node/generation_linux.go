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
	lock, err := openGenerationLock(lockPath)
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

// openGenerationLock verifies that allocation and mutation use a private,
// owned, regular, single-link inode. O_NOFOLLOW prevents last-component
// symlinks; O_NONBLOCK prevents a special-file open from hanging before stat.
// This does NOT defend mutable parent directories, bind mounts, or remote
// filesystem flock semantics. These remain target-HIL admission constraints.
func openGenerationLock(path string) (*os.File, error) {
    fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0o600)
    if err != nil {
        return nil, fmt.Errorf("open private generation lock: %w", err)
    }
    lock := os.NewFile(uintptr(fd), path)
    info, err := lock.Stat()
    if err != nil {
        _ = lock.Close()
        return nil, fmt.Errorf("stat generation lock: %w", err)
    }
    stat, ok := info.Sys().(*syscall.Stat_t)
    if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 ||
        stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
        _ = lock.Close()
        return nil, fmt.Errorf("generation lock must be an owned private single-link regular file")
    }
    return lock, nil
}

// withCurrentGenerationLease holds a read lock on the same state lock used by
// allocateGeneration for the entire admitted local callback. A new generation
// cannot be allocated concurrently with that callback on the same local
// filesystem. If this runtime has already been superseded, the callback is
// never entered, so replay and rendezvous state remain untouched.
//
// This is an OS-local same-state-path fence, NOT distributed leader election,
// cross-host state coherence or shutdown/drain qualification.
func withCurrentGenerationLease(path, nodeID string, expected uint64, fn func() error) error {
    if path == "" || nodeID == "" || expected == 0 || fn == nil {
        return fmt.Errorf("generation guard requires valid identity and callback")
    }
    lock, err := openGenerationLock(path+".lock")
    if err != nil {
        return fmt.Errorf("open generation guard lock: %w", err)
    }
    defer lock.Close()

    // Fail closed during writer rotation instead of waiting on a contested
    // generation lock. The shared lock must be held until fn returns.
    if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
        return fmt.Errorf("generation guard lock unavailable: %w", err)
    }
    defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

    raw, err := os.ReadFile(path)
    if err != nil {
        return fmt.Errorf("generation state unavailable: %w", err)
    }
    decoder := json.NewDecoder(bytesReader(raw))
    decoder.DisallowUnknownFields()
    var state generationState
    if err := decoder.Decode(&state); err != nil {
        return fmt.Errorf("invalid generation state: %w", err)
    }
    var trailing any
    if err := decoder.Decode(&trailing); err != io.EOF {
        return fmt.Errorf("generation state has trailing data")
    }
    if state.Schema != generationStateSchema ||
        state.NodeID != nodeID || state.Generation != expected {
        return fmt.Errorf("runtime generation superseded or inconsistent")
    }
    return fn()
}

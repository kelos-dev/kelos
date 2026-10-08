package controller

import "sync"

// BranchLocker tracks which task owns each concurrency group (by default a
// task's workspace+branch, see concurrencyGroup). TryAcquire atomically checks
// availability and claims ownership, so the hot path does not depend on the
// informer cache being up-to-date.
// The status-based check (checkConcurrencyLock) is kept as a fallback to
// reconstruct ownership after a controller restart.
type BranchLocker struct {
	mu     sync.Mutex
	owners map[string]string // lock key -> owning task name
}

// NewBranchLocker creates a new BranchLocker.
func NewBranchLocker() *BranchLocker {
	return &BranchLocker{owners: make(map[string]string)}
}

// TryAcquire attempts to claim the lock for the given task.
// Returns (true, "") if acquired, or (false, holder) if another task holds it.
// Calling TryAcquire again with the same key and taskName is idempotent.
func (bl *BranchLocker) TryAcquire(key, taskName string) (bool, string) {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	if holder, ok := bl.owners[key]; ok && holder != taskName {
		return false, holder
	}
	bl.owners[key] = taskName
	return true, ""
}

// Release releases the lock if held by the given task.
// It is safe to call Release even if the task does not hold the lock.
func (bl *BranchLocker) Release(key, taskName string) {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	if bl.owners[key] == taskName {
		delete(bl.owners, key)
	}
}

// Holder returns the task name holding the given key, or "" if unheld.
func (bl *BranchLocker) Holder(key string) string {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	return bl.owners[key]
}

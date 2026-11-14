package main

// queue.go: the download queue (A11). At most "Downloads at the same time"
// (Settings) files download at once; the rest wait as Queued and start by
// themselves, in queue order, when a place is free. The order can be
// changed with Move.

import (
	"errors"
	"sort"
)

// queueKey: the place of a job in the queue (lower = sooner). Jobs never
// moved keep the order they were added in.
func queueKey(j *Job, i int) int64 {
	if j.Order != 0 {
		return j.Order
	}
	return int64(i) + 1
}

// queuedLocked returns the waiting jobs in the order they will start.
func (m *Manager) queuedLocked() []*Job {
	type kj struct {
		k int64
		j *Job
	}
	var list []kj
	for i, j := range m.jobs {
		if j.Status == StatusQueued && !j.Removed {
			list = append(list, kj{queueKey(j, i), j})
		}
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].k < list[b].k })
	out := make([]*Job, len(list))
	for i, x := range list {
		out[i] = x.j
	}
	return out
}

// Move changes the place of a queued download: "first", "up", "down" or "last".
func (m *Manager) Move(id, where string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil {
		return errors.New("download not found")
	}
	if j.Status != StatusQueued {
		return errors.New("Only waiting (queued) downloads can be moved.")
	}
	q := m.queuedLocked()
	pos := -1
	for i, x := range q {
		if x == j {
			pos = i
		}
	}
	// Give every queued job an explicit place, then move.
	idx := map[*Job]int{}
	for i, x := range m.jobs {
		idx[x] = i
	}
	keys := make([]int64, len(q))
	for i, x := range q {
		keys[i] = queueKey(x, idx[x])
	}
	switch where {
	case "first":
		q = append([]*Job{j}, append(q[:pos:pos], q[pos+1:]...)...)
	case "last":
		q = append(append(q[:pos:pos], q[pos+1:]...), j)
	case "up":
		if pos > 0 {
			q[pos-1], q[pos] = q[pos], q[pos-1]
		}
	case "down":
		if pos < len(q)-1 {
			q[pos+1], q[pos] = q[pos], q[pos+1]
		}
	default:
		return errors.New("unknown move")
	}
	// Reuse the same set of keys (sorted), so the moved jobs keep their
	// place relative to downloads added later.
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	for i := 1; i < len(keys); i++ {
		if keys[i] <= keys[i-1] {
			keys[i] = keys[i-1] + 1
		}
	}
	for i, x := range q {
		x.Order = keys[i]
	}
	m.saveHistoryLocked()
	return nil
}

package main

// Windows taskbar progress for the main BDDM window (item 30).
// The visible window is an Edge/Chrome app window; the progress overlay
// is applied to that HWND.

const (
	taskbarNone          = "none"
	taskbarNormal        = "normal"
	taskbarPaused        = "paused"
	taskbarError         = "error"
	taskbarIndeterminate = "indeterminate"
)

type taskbarProgress struct {
	State string
	Done  uint64
	Total uint64
}

func taskbarFromJobs(jobs []JobView) taskbarProgress {
	var down, paused, failed int
	var done, total uint64
	unknown := false
	for _, j := range jobs {
		switch j.Status {
		case StatusDownloading:
			down++
			if j.Size > 0 {
				total += uint64(j.Size)
				d := j.Done
				if d < 0 {
					d = 0
				}
				if d > j.Size {
					d = j.Size
				}
				done += uint64(d)
			} else {
				unknown = true
			}
		case StatusPaused:
			if j.Size <= 0 || j.Done >= j.Size {
				continue
			}
			paused++
			total += uint64(j.Size)
			d := j.Done
			if d < 0 {
				d = 0
			}
			done += uint64(d)
		case StatusError:
			if !j.ViewOnly {
				failed++
			}
		}
	}
	switch {
	case down > 0 && (unknown && total == 0):
		return taskbarProgress{State: taskbarIndeterminate}
	case down > 0 && total > 0:
		return taskbarProgress{State: taskbarNormal, Done: done, Total: total}
	case down > 0:
		return taskbarProgress{State: taskbarIndeterminate}
	case paused > 0:
		return taskbarProgress{State: taskbarPaused, Done: done, Total: total}
	case failed > 0:
		return taskbarProgress{State: taskbarError, Done: 1, Total: 1}
	default:
		return taskbarProgress{State: taskbarNone}
	}
}

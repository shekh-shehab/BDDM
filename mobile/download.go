package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const userAgent = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Mobile Safari/537.36"

type partState struct {
	From int64 `json:"from"`
	To   int64 `json:"to"` // inclusive
	Done int64 `json:"done"`
}

type diskState struct {
	URL  string      `json:"url"`
	Size int64       `json:"size"`
	ETag string      `json:"etag,omitempty"`
	Mod  string      `json:"lastModified,omitempty"`
	Name string      `json:"name"`
	Parts []partState `json:"parts"`
}

type probeInfo struct {
	Size         int64
	Filename     string
	ETag         string
	LastModified string
	CanRange     bool
	Type         string
	FinalURL     string
}

func (e *Engine) download(ctx context.Context, j *job) error {
	j.setMessage("Connecting...")
	e.notify()
	info, err := e.probe(ctx, j.URL, j.Headers)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}

	e.mu.Lock()
	dir := j.Dir
	if dir == "" || j.AutoDir {
		dir = e.settings.dirFor(firstNonEmpty(j.Want, info.Filename))
		j.Dir = dir
		j.AutoDir = false
	}
	name := j.Name
	if name == "" {
		name = firstNonEmpty(j.Want, info.Filename, filenameFromURL(j.URL), "download")
		name = e.uniqueNameLocked(dir, name, j.ID)
		j.Name = name
	}
	j.Size = info.Size
	partsN := j.Parts
	if partsN < 1 {
		partsN = e.settings.Parts
	}
	e.mu.Unlock()

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create folder: %w", err)
	}

	full := filepath.Join(dir, name)
	partPath := full + ".part"
	statePath := full + ".bddm.json"

	var parts []partState
	resuming := false
	if st, err := loadDiskState(statePath); err == nil && (st.URL == j.URL || st.Name == name) && fileExists(partPath) {
		if st.Size == info.Size && (info.ETag == "" || st.ETag == "" || st.ETag == info.ETag) {
			parts = st.Parts
			resuming = true
		} else {
			_ = os.Remove(partPath)
			_ = os.Remove(statePath)
		}
	}
	if !resuming {
		if info.CanRange && info.Size > 0 && partsN > 1 && info.Size > 1<<20 {
			parts = planParts(info.Size, partsN)
		} else {
			to := info.Size - 1
			if to < 0 {
				to = -1
			}
			parts = []partState{{From: 0, To: to, Done: 0}}
		}
	}

	var already int64
	for _, p := range parts {
		already += p.Done
	}
	j.mu.Lock()
	j.Done = already
	j.Size = info.Size
	j.xferStart = time.Now()
	j.xferFrom = already
	j.mu.Unlock()

	// Allocate the .part file.
	f, err := os.OpenFile(partPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("cannot create file: %w", err)
	}
	defer f.Close()
	if info.Size > 0 {
		if st, _ := f.Stat(); st.Size() < info.Size {
			if err := f.Truncate(info.Size); err != nil {
				// Some filesystems refuse preallocation; try without.
				_ = err
			}
		}
	}

	save := func() {
		_ = writeDiskState(statePath, &diskState{
			URL: j.URL, Size: info.Size, ETag: info.ETag, Mod: info.LastModified, Name: name, Parts: parts,
		})
	}
	save()

	var done atomic.Int64
	done.Store(already)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	stop := ctx.Done()

	runPart := func(i int) {
		defer wg.Done()
		var partErr error
		for try := 1; try <= 5; try++ {
			if ctx.Err() != nil {
				return
			}
			err := e.fetchPart(ctx, f, j.URL, j.Headers, &parts[i], info, &done, func() {
				e.publishProgress(j, parts, info.Size, &done)
			})
			if err == nil || ctx.Err() != nil {
				return
			}
			partErr = err
			e.publishProgress(j, parts, info.Size, &done)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(try) * 800 * time.Millisecond):
			}
		}
		if partErr != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = partErr
			}
			mu.Unlock()
		}
	}

	// Single unknown-size stream: one connection, no pre-split.
	if len(parts) == 1 && parts[0].To < 0 {
		wg.Add(1)
		runPart(0)
	} else {
		for i := range parts {
			if parts[i].To >= 0 && parts[i].From+parts[i].Done > parts[i].To {
				continue
			}
			wg.Add(1)
			go runPart(i)
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		save()
		return ctx.Err()
	}
	if firstErr != nil && done.Load() < info.Size && info.Size > 0 {
		save()
		return firstErr
	}
	// Unknown size: final length is what we wrote.
	final := done.Load()
	if info.Size <= 0 {
		_ = f.Truncate(final)
		info.Size = final
	}
	if err := f.Sync(); err != nil {
		return err
	}
	f.Close()
	if err := os.Rename(partPath, full); err != nil {
		return fmt.Errorf("could not finish the file: %w", err)
	}
	_ = os.Remove(statePath)
	j.mu.Lock()
	j.Done = final
	j.Size = info.Size
	j.speed = 0
	j.segs = nil
	j.mu.Unlock()
	_ = stop
	return nil
}

func (e *Engine) publishProgress(j *job, parts []partState, size int64, done *atomic.Int64) {
	got := done.Load()
	j.mu.Lock()
	j.Done = got
	j.Size = size
	elapsed := time.Since(j.xferStart).Seconds()
	if elapsed > 0.2 {
		j.speed = float64(got-j.xferFrom) / elapsed
	}
	if len(parts) >= 2 && size > 0 {
		info := &SegInfo{Kind: "parts", Segs: make([]SegView, 0, len(parts))}
		for _, p := range parts {
			w := p.To - p.From + 1
			if w < 0 {
				w = 0
			}
			st := "wait"
			switch {
			case p.To >= 0 && p.From+p.Done > p.To:
				st = "done"
				p.Done = w
			case p.Done > 0 && p.Done < w:
				st = "active"
			}
			info.Segs = append(info.Segs, SegView{W: w, Done: p.Done, State: st})
		}
		info.count()
		j.segs = info
	}
	j.mu.Unlock()
	e.notify()
}

func (e *Engine) fetchPart(ctx context.Context, f *os.File, rawURL string, headers map[string]string, p *partState, info probeInfo, done *atomic.Int64, tick func()) error {
	from := p.From + p.Done
	to := p.To
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	applyHeaders(req, headers)
	if to >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	} else if from > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
	}
	resp, err := e.client.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("the server returned HTTP %d", resp.StatusCode)
	}
	off := from
	buf := make([]byte, 64*1024)
	r := io.Reader(resp.Body)
	r = limitedReader{r: r, l: e.limit, stop: ctx.Done()}
	lastTick := time.Now()
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], off); werr != nil {
				return werr
			}
			off += int64(n)
			atomicAdd := int64(n)
			// If we already counted some Done, only add new bytes.
			p.Done = off - p.From
			done.Add(atomicAdd)
			if tick != nil && time.Since(lastTick) >= 400*time.Millisecond {
				tick()
				lastTick = time.Now()
			}
		}
		if err == io.EOF {
			if tick != nil {
				tick()
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// fetchPart's done accounting is wrong if we resume (already counted p.Done).
// I increment done by n only for new bytes; initial already was stored in done
// at start. Good.

func planParts(size int64, n int) []partState {
	if n < 1 {
		n = 1
	}
	// Don't make tiny parts.
	const minPart = int64(256 << 10)
	if size/int64(n) < minPart {
		n = int(size / minPart)
		if n < 1 {
			n = 1
		}
	}
	parts := make([]partState, n)
	each := size / int64(n)
	var start int64
	for i := 0; i < n; i++ {
		end := start + each - 1
		if i == n-1 {
			end = size - 1
		}
		parts[i] = partState{From: start, To: end}
		start = end + 1
	}
	return parts
}

func (e *Engine) probe(ctx context.Context, rawURL string, headers map[string]string) (probeInfo, error) {
	// Prefer a 1-byte range GET: some servers lie on HEAD and this also
	// tells us whether Range works.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return probeInfo{}, err
	}
	applyHeaders(req, headers)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := e.client.c.Do(req)
	if err != nil {
		return probeInfo{}, fmt.Errorf("could not connect: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16))
	resp.Body.Close()

	info := probeInfo{
		Filename:     firstNonEmpty(filenameFromDisposition(resp.Header.Get("Content-Disposition")), filenameFromURL(rawURL)),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		Type:         resp.Header.Get("Content-Type"),
		FinalURL:     rawURL,
	}
	if resp.Request != nil && resp.Request.URL != nil {
		info.FinalURL = resp.Request.URL.String()
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		info.CanRange = true
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if i := strings.LastIndex(cr, "/"); i >= 0 {
				if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
					info.Size = n
				}
			}
		}
	case http.StatusOK:
		info.CanRange = strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes")
		info.Size = resp.ContentLength
		if info.Size < 0 {
			info.Size = 0
		}
	default:
		return probeInfo{}, fmt.Errorf("the server returned HTTP %d", resp.StatusCode)
	}
	if info.Filename == "" {
		info.Filename = "download"
	}
	return info, nil
}

func (e *Engine) uniqueNameLocked(dir, name, exceptID string) string {
	name = sanitizeName(name)
	if name == "" {
		name = "download"
	}
	if !e.nameInUseLocked(dir, name, exceptID) && !fileExists(filepath.Join(dir, name)) {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 500; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !e.nameInUseLocked(dir, cand, exceptID) && !fileExists(filepath.Join(dir, cand)) {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d%s", stem, time.Now().Unix(), ext)
}

func (e *Engine) nameInUseLocked(dir, name, exceptID string) bool {
	for _, j := range e.jobs {
		if j.ID != exceptID && j.Dir == dir && j.Name == name && j.Status != StatusError {
			return true
		}
	}
	return false
}

func loadDiskState(p string) (*diskState, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var st diskState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeDiskState(p string, st *diskState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0644)
}


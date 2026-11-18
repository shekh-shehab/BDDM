package main

// server.go: the local web server on 127.0.0.1:9614. It serves the window's
// page (ui/index.html) and a small JSON API. The Chrome extension talks to
// POST /add and the small /ext/... endpoints below.

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed ui
var uiFiles embed.FS

type server struct {
	m    *Manager
	quit func()
	port string
}

func newServer(m *Manager, port string, quit func()) http.Handler {
	s := &server{m: m, quit: quit, port: port}
	mux := http.NewServeMux()

	ui, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("GET /", http.FileServer(http.FS(ui)))

	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"app": "BDDM", "version": appVersion})
	})
	mux.HandleFunc("GET /api/list", func(w http.ResponseWriter, r *http.Request) {
		after := int64(-1)
		if v := r.URL.Query().Get("events"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				after = n
			}
		}
		events, seq := m.Events(after)
		writeJSON(w, 200, map[string]any{"jobs": m.List(), "groups": m.Groups(), "events": events, "eventSeq": seq, "eventBoot": bootID})
	})
	// Folder downloads (one row in the list for all files of a Drive folder).
	mux.HandleFunc("POST /api/groups/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		reply(w, m.PauseGroup(r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/groups/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		reply(w, m.ResumeGroup(r.PathValue("id"), false))
	})
	mux.HandleFunc("POST /api/groups/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		reply(w, m.ResumeGroup(r.PathValue("id"), true))
	})
	mux.HandleFunc("POST /api/groups/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeleteFile bool `json:"deleteFile"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, m.DeleteGroup(r.PathValue("id"), body.DeleteFile))
	})
	mux.HandleFunc("POST /api/groups/{id}/open", func(w http.ResponseWriter, r *http.Request) {
		dir, err := m.GroupDir(r.PathValue("id"))
		if err == nil {
			err = openFolder(dir, "")
		}
		reply(w, err)
	})
	mux.HandleFunc("POST /api/add", s.handlePasted)
	mux.HandleFunc("POST /add", s.handleCaptured) // for the browser extension

	// Endpoints for the Chrome extension.
	mux.HandleFunc("GET /ext/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"app": "BDDM", "ok": true, "version": appVersion})
	})
	// GET /ext/status?ids=a,b,c -> the state of the jobs the extension sent.
	mux.HandleFunc("GET /ext/status", func(w http.ResponseWriter, r *http.Request) {
		want := map[string]bool{}
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if id = strings.TrimSpace(id); id != "" {
				want[id] = true
			}
		}
		out := []map[string]any{}
		for _, j := range m.Views(want) {
			out = append(out, map[string]any{"id": j.ID, "status": j.Status, "error": j.Error,
				"name": j.Name, "url": j.URL, "removed": j.Removed})
			delete(want, j.ID)
		}
		missing := []string{}
		for id := range want {
			missing = append(missing, id)
		}
		writeJSON(w, 200, map[string]any{"jobs": out, "missing": missing})
	})
	// The extension gave a failed job back to the browser: remove it from the
	// list and delete its unfinished .part file (it stays in History).
	// Finished downloads are never touched here.
	mux.HandleFunc("POST /ext/jobs/{id}/handedback", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		for _, j := range m.Views(map[string]bool{id: true}) {
			if j.ID == id {
				if j.Status == StatusError {
					reply(w, m.Delete(id, true))
				} else {
					reply(w, nil)
				}
				return
			}
		}
		reply(w, errors.New("download not found"))
	})
	// Save as PDF for view-only Drive files (pdf.go).
	mux.HandleFunc("POST /ext/pdf/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name  string `json:"name"`
			URL   string `json:"url"`
			Pages int    `json:"pages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			reply(w, errors.New("bad request"))
			return
		}
		id, err := m.PDFStart(body.Name, body.URL, body.Pages)
		if err != nil {
			reply(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "id": id})
	})
	mux.HandleFunc("POST /ext/pdf/{id}/page", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			N    int    `json:"n"`
			Data string `json:"data"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, pdfMaxPageLen)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			reply(w, errors.New("the page image was too big or damaged"))
			return
		}
		reply(w, m.PDFPage(r.PathValue("id"), body.N, body.Data))
	})
	mux.HandleFunc("POST /ext/pdf/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		var o PDFOptions
		_ = json.NewDecoder(r.Body).Decode(&o)
		j, pages, err := m.PDFFinish(r.PathValue("id"), o)
		if err != nil {
			reply(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "name": j.Name, "dir": j.Dir, "id": j.ID, "pages": pages})
	})
	mux.HandleFunc("POST /ext/pdf/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		m.PDFCancel(r.PathValue("id"))
		reply(w, nil)
	})
	mux.HandleFunc("POST /ext/show", func(w http.ResponseWriter, r *http.Request) {
		reply(w, openAppWindow("http://127.0.0.1:"+s.port))
	})
	mux.HandleFunc("GET /api/check", func(w http.ResponseWriter, r *http.Request) {
		d := m.FindDone(strings.TrimSpace(r.URL.Query().Get("url")))
		res := map[string]any{"duplicate": d != nil}
		if d != nil {
			res["name"] = d.Name
		}
		writeJSON(w, 200, res)
	})
	// The video options window (ui/video.html) and the video tools.
	mux.HandleFunc("GET /api/video/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.VideoPending(r.PathValue("id"))
		if err != nil {
			writeJSON(w, 404, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/video/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			VideoOptions
			Dir   string `json:"dir"`
			Later bool   `json:"later"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
		job, err := m.StartVideo(r.PathValue("id"), body.VideoOptions, body.Dir, body.Later)
		if err != nil {
			reply(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "job": job})
	})
	mux.HandleFunc("POST /api/video/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		reply(w, m.RetryVideoInfo(r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/tools", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, m.tools.Status())
	})
	mux.HandleFunc("POST /api/tools/install", func(w http.ResponseWriter, r *http.Request) {
		reply(w, m.tools.Install())
	})

	// The "Download File Info" window (ui/confirm.html).
	mux.HandleFunc("GET /api/pending/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Pending(r.PathValue("id"))
		if err != nil {
			writeJSON(w, 404, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/pending/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name    string   `json:"name"`
			Dir     string   `json:"dir"`
			Later   bool     `json:"later"`
			Format  string   `json:"format"`
			Hash    string   `json:"hash"`
			Mirrors []string `json:"mirrors"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
		if body.Hash != "" || len(body.Mirrors) > 0 {
			if err := m.SetPendingExtras(r.PathValue("id"), body.Hash, body.Mirrors); err != nil {
				reply(w, err)
				return
			}
		}
		job, err := m.StartPending(r.PathValue("id"), body.Name, body.Dir, body.Later, body.Format)
		if err != nil {
			reply(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "job": job})
	})
	mux.HandleFunc("GET /api/drive/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.DriveFolder(r.PathValue("id"))
		if err != nil {
			writeJSON(w, 404, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/drive/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs   []string `json:"ids"`
			Dir   string   `json:"dir"`
			Later bool     `json:"later"`
			Again bool     `json:"again"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body)
		res, err := m.StartDriveFolder(r.PathValue("id"), body.IDs, body.Dir, body.Later, body.Again)
		if err != nil {
			reply(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "result": res})
	})
	mux.HandleFunc("POST /api/drive/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		m.CancelDriveFolder(r.PathValue("id"))
		reply(w, nil)
	})
	mux.HandleFunc("POST /api/pending/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		reply(w, m.CancelPending(r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/jobs/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		reply(w, m.Pause(r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/jobs/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		reply(w, m.Resume(r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/jobs/{id}/move", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Where string `json:"where"` // first, up, down, last
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body)
		reply(w, m.Move(r.PathValue("id"), body.Where))
	})
	mux.HandleFunc("POST /api/jobs/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeleteFile bool `json:"deleteFile"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, m.Delete(r.PathValue("id"), body.DeleteFile))
	})
	mux.HandleFunc("POST /api/jobs/{id}/open", func(w http.ResponseWriter, r *http.Request) {
		dir, file, err := m.FilePath(r.PathValue("id"))
		if err == nil {
			err = openFolder(dir, file)
		}
		reply(w, err)
	})
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"jobs": m.History(r.URL.Query().Get("q"))})
	})
	mux.HandleFunc("POST /api/history/clear", func(w http.ResponseWriter, r *http.Request) {
		m.ClearHistory()
		reply(w, nil)
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, m.Settings())
	})
	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		st := m.Settings() // fields the page does not send keep their value
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			reply(w, errors.New("bad settings"))
			return
		}
		saved, err := m.SetSettings(st)
		if err != nil {
			reply(w, err)
			return
		}
		writeJSON(w, 200, saved)
	})
	mux.HandleFunc("POST /api/pickfolder", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Start string `json:"start"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
		start := strings.TrimSpace(body.Start)
		if start == "" {
			start = m.Settings().DownloadDir
		}
		dir, err := pickFolder(start)
		if err != nil {
			reply(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"dir": dir})
	})
	mux.HandleFunc("POST /api/openfolder", func(w http.ResponseWriter, r *http.Request) {
		reply(w, openFolder(m.Settings().DownloadDir, ""))
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		reply(w, nil)
		go s.quit()
	})
	return s.guard(mux)
}

// guard only lets this computer's own window (and browser extensions) talk to
// the API, so random websites cannot add downloads or delete files.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		ext := strings.HasPrefix(origin, "chrome-extension://") || strings.HasPrefix(origin, "moz-extension://") ||
			strings.HasPrefix(origin, "extension://")
		extPath := r.URL.Path == "/add" || strings.HasPrefix(r.URL.Path, "/ext/")
		if extPath && ext {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions { // CORS preflight
				w.WriteHeader(http.StatusNoContent)
				return
			}
		} else if origin != "" &&
			origin != "http://127.0.0.1:"+s.port && origin != "http://localhost:"+s.port {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path != "/api/quit" {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && r.ContentLength != 0 {
				http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// isVideoRequest decides whether a link goes to the video window.
func isVideoRequest(req AddRequest) bool {
	if req.Kind == "media" || (req.Kind == "video" && req.Stream == "" && isDirectMediaURL(req.URL)) {
		// A video/audio file itself (not a page or a stream): the fast
		// engine downloads it (several connections, resumable) with the
		// browser's headers, like IDM. yt-dlp is not needed.
		return false
	}
	return req.Kind == "video" || isVideoPage(req.URL) || isStreamURL(req.URL) || req.Stream != ""
}

// isDirectMediaURL: a link to a video or audio file (by its extension).
func isDirectMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || isVideoPage(raw) || isStreamURL(raw) || isYouTube(raw) {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".mp4", ".m4v", ".webm", ".mkv", ".mov", ".avi", ".flv", ".wmv", ".mpg", ".mpeg", ".3gp", ".ogv",
		".mp3", ".m4a", ".ogg", ".oga", ".opus", ".wav", ".flac", ".aac":
		return true
	}
	return false
}

func (s *server) addVideo(w http.ResponseWriter, req AddRequest) {
	job, err := s.m.AddVideoPending(req)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	u := "http://127.0.0.1:" + s.port + "/video.html?id=" + job.ID
	if err := openAppWindowSize(u, 720, 760); err != nil {
		log.Printf("could not open the video window: %v (open %s)", err, u)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "pending": true, "video": true, "job": job, "window": u})
}

// handlePasted handles links pasted into the app window: files start at
// once; video pages open the video options window.
func (s *server) handlePasted(w http.ResponseWriter, r *http.Request) {
	var req AddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "Bad request: expected JSON {url}"})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if driveNonFilePage(req.URL) {
		writeJSON(w, 400, map[string]any{"ok": false, "error": errDrivePage.Error()})
		return
	}
	if s.handleDrive(w, req, false) {
		return
	}
	if oneShotPage(req.URL) {
		writeJSON(w, 400, map[string]any{"ok": false, "error": msgOneShotPage})
		return
	}
	if isOneShot(req.URL, req.Referer) {
		s.addFile(w, req) // no look at the link first: it works only once
		return
	}
	extras := len(req.Mirrors) > 0 || strings.TrimSpace(req.Hash) != ""
	if !extras && (isVideoRequest(req) || looksLikeWebPage(s.m.client, req.URL)) {
		s.addVideo(w, req)
		return
	}
	s.addFile(w, req)
}

// handleCaptured handles links from the browser extension. With "Ask before
// starting captured downloads" on, the job waits in the "Download File Info"
// window; nothing is downloaded until the user presses Start.
func (s *server) handleCaptured(w http.ResponseWriter, r *http.Request) {
	var req AddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "Bad request: expected JSON {url, referer, cookie, filename}"})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if driveNonFilePage(req.URL) {
		writeJSON(w, 400, map[string]any{"ok": false, "error": errDrivePage.Error()})
		return
	}
	if req.Kind != "video" && s.handleDrive(w, req, true) {
		return
	}
	if isVideoRequest(req) {
		s.addVideo(w, req)
		return
	}
	// A new link for a download whose link expired: continue that one.
	if v := s.m.RefreshLink(req, captureName(req), 0); v != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "job": v, "refreshed": true})
		return
	}
	if !s.m.Settings().AskCaptured {
		s.addFile(w, req)
		return
	}
	job, err := s.m.AddPending(req)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	u := "http://127.0.0.1:" + s.port + "/confirm.html?id=" + job.ID
	if err := openAppWindowSize(u, 600, 520); err != nil {
		// No window could be shown: do not lose the download, start it.
		log.Printf("could not open the confirm window: %v; starting the download", err)
		if started, err := s.m.StartPending(job.ID, "", "", false, ""); err == nil {
			job = started
		}
		writeJSON(w, 200, map[string]any{"ok": true, "job": job})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "pending": true, "job": job})
}

// captureName is the file name a captured download will most likely get.
func captureName(req AddRequest) string {
	if n := sanitizeName(req.Filename); n != "" {
		return n
	}
	return filenameFromURL(req.URL)
}

// handleDrive handles Google Drive links: folders open the folder window,
// Docs/Sheets/Slides open the "Download File Info" window with the export
// formats, files go on like other files (captured=true: the extension's
// "ask before starting" setting applies). Reports whether it answered.
func (s *server) handleDrive(w http.ResponseWriter, req AddRequest, captured bool) bool {
	l, ok := parseDriveLink(req.URL)
	if !ok {
		return false
	}
	switch l.Kind {
	case "folder":
		id, err := s.m.AddDriveFolder(req, l)
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
			return true
		}
		u := "http://127.0.0.1:" + s.port + "/drive.html?id=" + id
		if err := openAppWindowSize(u, 760, 720); err != nil {
			log.Printf("could not open the Drive folder window: %v (open %s)", err, u)
		}
		writeJSON(w, 200, map[string]any{"ok": true, "pending": true, "window": u,
			"message": "Choose the files in the Google Drive folder window."})
		return true
	case "doc":
		s.addPendingWindow(w, req)
		return true
	}
	// A file.
	if captured && s.m.Settings().AskCaptured {
		s.addPendingWindow(w, req)
		return true
	}
	s.addFile(w, req)
	return true
}

// addPendingWindow adds a download that waits in the "Download File Info"
// window.
func (s *server) addPendingWindow(w http.ResponseWriter, req AddRequest) {
	job, err := s.m.AddPending(req)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	u := "http://127.0.0.1:" + s.port + "/confirm.html?id=" + job.ID
	if err := openAppWindowSize(u, 600, 560); err != nil {
		log.Printf("could not open the confirm window: %v; starting the download", err)
		if started, err := s.m.StartPending(job.ID, "", "", false, ""); err == nil {
			job = started
		}
		writeJSON(w, 200, map[string]any{"ok": true, "job": job})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "pending": true, "job": job, "window": u,
		"message": "Choose where to save it in the BDDM window."})
}

func (s *server) addFile(w http.ResponseWriter, req AddRequest) {
	job, err := s.m.Add(req)
	var dup *DuplicateError
	if errors.As(err, &dup) {
		writeJSON(w, 409, map[string]any{"ok": false, "duplicate": true, "name": dup.Name, "error": dup.Error()})
		return
	}
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "job": job})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func reply(w http.ResponseWriter, err error) {
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// looksLikeWebPage asks the server what a pasted link is: a web page
// (text/html) is probably a video page for yt-dlp; anything else is a file.
func looksLikeWebPage(client *http.Client, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	switch ext := strings.ToLower(path.Ext(u.Path)); ext {
	case "", ".html", ".htm", ".php", ".asp", ".aspx", ".jsp", ".shtml":
	default:
		return false // looks like a file name
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req, err := newRequest(ctx, method, raw, reqHeaders{})
		if err != nil {
			return false
		}
		if method == http.MethodGet {
			req.Header.Set("Range", "bytes=0-0")
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			continue
		}
		return strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html")
	}
	return false
}

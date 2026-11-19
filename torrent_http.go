package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
)

func (s *server) torrentWindow(id string) string {
	return "http://127.0.0.1:" + s.port + "/torrent.html?id=" + id
}

func (s *server) openTorrentWindow(id string) {
	u := s.torrentWindow(id)
	if err := openAppWindowSize(u, 560, 620); err != nil {
		log.Printf("could not open the torrent window: %v (open %s)", err, u)
	}
}

func (s *server) replyTorrent(w http.ResponseWriter, job JobView, err error) {
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.openTorrentWindow(job.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "pending": true, "torrent": true, "job": job, "window": s.torrentWindow(job.ID)})
}

func (s *server) addTorrentFromPaste(w http.ResponseWriter, raw string) {
	raw = strings.TrimSpace(raw)
	var job JobView
	var err error
	if isMagnet(raw) {
		job, err = s.m.AddTorrentMagnet(raw)
	} else {
		job, err = s.m.AddTorrentFile(raw, nil)
	}
	s.replyTorrent(w, job, err)
}

func (s *server) handleTorrentAdd(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "Could not read the torrent file."})
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "Please choose a .torrent file."})
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 16<<20))
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "Could not read the torrent file."})
			return
		}
		name := ""
		if hdr != nil {
			name = hdr.Filename
		}
		_ = name
		job, err := s.m.AddTorrentFile("", data)
		s.replyTorrent(w, job, err)
		return
	}
	var body struct {
		URL    string `json:"url"`
		Magnet string `json:"magnet"`
		Path   string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "Bad request"})
		return
	}
	raw := strings.TrimSpace(firstNonEmpty(body.Magnet, body.URL, body.Path))
	if raw == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "Please paste a magnet link or choose a .torrent file."})
		return
	}
	s.addTorrentFromPaste(w, raw)
}

func (s *server) handleTorrentPick(w http.ResponseWriter, r *http.Request) {
	path, err := pickTorrentFile()
	if err != nil {
		reply(w, err)
		return
	}
	if path == "" {
		writeJSON(w, 200, map[string]any{"ok": true, "cancelled": true})
		return
	}
	job, err := s.m.AddTorrentFile(path, nil)
	s.replyTorrent(w, job, err)
}

func (s *server) handleTorrentPending(w http.ResponseWriter, r *http.Request) {
	v, err := s.m.TorrentPending(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) handleTorrentStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir string `json:"dir"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	job, err := s.m.StartTorrent(r.PathValue("id"), body.Dir)
	if err != nil {
		reply(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "job": job})
}

func (s *server) handleTorrentCancel(w http.ResponseWriter, r *http.Request) {
	reply(w, s.m.CancelTorrent(r.PathValue("id")))
}

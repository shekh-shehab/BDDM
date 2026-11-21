package main

// torrent.go: BitTorrent downloads and seeding (roadmap step 3 / v1.4.5).
// Torrents appear in the main list, use the usual download folders, and keep
// seeding for as long as BDDM runs. There is no automatic stop by ratio or
// time. State is saved under the config directory (%APPDATA%\BDDM on Windows).

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

const (
	kindTorrent       = "torrent"
	StatusSeeding     = "Seeding"
	torrentListenPort = 6881
)

// Aliases so engine.go can hold these handles without importing the library.
type torrentHandle = *torrent.Torrent
type torrentClient = *torrent.Client
type torrentPieceStore = storage.PieceCompletion

// testTorrentLoopback is set while `go test` runs so tests never open DHT,
// UPnP or a public listen address.
var testTorrentLoopback bool

func init() {
	if flag.Lookup("test.v") != nil {
		testTorrentLoopback = true
	}
}

// TorrentInfo is the torrent-specific data stored with a job in history.json.
type TorrentInfo struct {
	InfoHash string `json:"infoHash"`
	MetaFile string `json:"metaFile,omitempty"` // file name under configDir/torrents
	Files    int    `json:"files,omitempty"`
	Uploaded int64  `json:"uploaded,omitempty"`
	Single   bool   `json:"single,omitempty"`
}

// TorrentFileView is one file inside a torrent, shown in the add window.
type TorrentFileView struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// TorrentPendingView is what the add-torrent window receives.
type TorrentPendingView struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Size   int64             `json:"size"`
	Files  []TorrentFileView `json:"files"`
	Ready  bool              `json:"ready"`
	Source string            `json:"source"` // "magnet" or "file"
	URL    string            `json:"url"`
	Dir    string            `json:"dir"`
	Error  string            `json:"error,omitempty"`
	Status string            `json:"status"`
}

type parsedMagnet struct {
	Hash string
	Name string
	URI  string
}

func isMagnet(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "magnet:")
}

func looksLikeTorrentPath(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	s = strings.TrimPrefix(s, "file://")
	if strings.HasPrefix(s, "/") && strings.Contains(s, ":") && len(s) > 3 && s[2] == ':' {
		// file:///C:/foo.torrent
		s = s[1:]
	}
	if !strings.EqualFold(filepath.Ext(s), ".torrent") {
		return false
	}
	return filepath.IsAbs(s) || fileExists(s)
}

// parseMagnet extracts the info hash and display name from a magnet link.
func parseMagnet(raw string) (parsedMagnet, error) {
	raw = strings.TrimSpace(raw)
	m, err := metainfo.ParseMagnetV2Uri(raw)
	if err != nil {
		return parsedMagnet{}, errors.New("This is not a valid magnet link.")
	}
	var hash string
	if m.InfoHash.Ok {
		hash = strings.ToLower(m.InfoHash.Value.HexString())
	} else if m.V2InfoHash.Ok {
		hash = strings.ToLower(m.V2InfoHash.Value.HexString())
	} else {
		return parsedMagnet{}, errors.New("This magnet link has no info hash.")
	}
	name := sanitizeName(m.DisplayName)
	if name == "" {
		short := hash
		if len(short) > 8 {
			short = short[:8]
		}
		name = "Torrent " + short
	}
	return parsedMagnet{Hash: hash, Name: name, URI: m.String()}, nil
}

func (m *Manager) torrentsDir() string { return filepath.Join(m.configDir, "torrents") }

// torrentPieceDBName is the file name anacrolix gives a bolt piece-completion store.
const torrentPieceDBName = ".torrent.bolt.db"

func safeInfoHash(hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) < 8 || len(hash) > 64 {
		return ""
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return hash
}

func (m *Manager) pieceCompletionDir(hash string) string {
	hash = safeInfoHash(hash)
	if hash == "" {
		return ""
	}
	return filepath.Join(m.torrentsDir(), hash)
}

func copyFileIfMissing(src, dst string) error {
	if fileExists(dst) {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return writeFileSync(dst, data)
}

// migrateOldPieceCompletionLocked copies an old leftover .torrent.bolt.db from
// the user's download folder into the BDDM data folder, then removes the leftover
// once every known torrent in that folder has a copy.
func (m *Manager) migrateOldPieceCompletionLocked(downloadDir, hash string) {
	downloadDir = strings.TrimSpace(downloadDir)
	if downloadDir == "" {
		return
	}
	src := filepath.Join(downloadDir, torrentPieceDBName)
	if !fileExists(src) {
		return
	}
	hashes := map[string]bool{}
	if h := safeInfoHash(hash); h != "" {
		hashes[h] = true
	}
	wantDir := filepath.Clean(downloadDir)
	for _, j := range m.jobs {
		if j == nil || j.Kind != kindTorrent || j.Removed || j.Torrent == nil {
			continue
		}
		if j.Dir != "" && filepath.Clean(j.Dir) == wantDir {
			if h := safeInfoHash(j.Torrent.InfoHash); h != "" {
				hashes[h] = true
			}
		}
	}
	if len(hashes) == 0 {
		return
	}
	ok := true
	for h := range hashes {
		dest := filepath.Join(m.pieceCompletionDir(h), torrentPieceDBName)
		if err := copyFileIfMissing(src, dest); err != nil {
			log.Printf("could not copy torrent resume data out of %s: %v", downloadDir, err)
			ok = false
		}
	}
	if !ok {
		return
	}
	for h := range hashes {
		if !fileExists(filepath.Join(m.pieceCompletionDir(h), torrentPieceDBName)) {
			return
		}
	}
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		log.Printf("could not remove leftover %s: %v", src, err)
	}
}

// pieceCompletionRef is a view of a store the Manager owns. Close is a no-op so
// a storage client can be dropped without closing the shared bolt database.
type pieceCompletionRef struct {
	storage.PieceCompletion
}

func (pieceCompletionRef) Close() error { return nil }

func (p pieceCompletionRef) Persistent() bool {
	if c, ok := p.PieceCompletion.(storage.PieceCompletionPersistenter); ok {
		return c.Persistent()
	}
	return true
}

func (p pieceCompletionRef) Checkpoint(keys []metainfo.PieceKey) error {
	if c, ok := p.PieceCompletion.(storage.PieceCompletionCheckpointer); ok {
		return c.Checkpoint(keys)
	}
	return nil
}

func (p pieceCompletionRef) SetBatch(changes []storage.PieceCompletionChange) error {
	if c, ok := p.PieceCompletion.(storage.PieceCompletionBatchSetter); ok {
		return c.SetBatch(changes)
	}
	for _, ch := range changes {
		if err := p.PieceCompletion.Set(ch.Key, g.Some(ch.Complete)); err != nil {
			return err
		}
	}
	return nil
}

// pieceCompletionLocked opens (or reuses) the bolt store for one torrent under
// the BDDM data folder. m.mu must be held.
func (m *Manager) pieceCompletionLocked(hash string) (storage.PieceCompletion, error) {
	hash = safeInfoHash(hash)
	if hash == "" {
		return storage.NewMapPieceCompletion(), nil
	}
	if m.tPieceComp != nil {
		if pc, ok := m.tPieceComp[hash]; ok && pc != nil {
			return pieceCompletionRef{pc}, nil
		}
	}
	dir := m.pieceCompletionDir(hash)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	pc, err := storage.NewBoltPieceCompletion(dir)
	if err != nil {
		return nil, fmt.Errorf("could not open torrent resume data: %w", err)
	}
	if m.tPieceComp == nil {
		m.tPieceComp = map[string]torrentPieceStore{}
	}
	m.tPieceComp[hash] = pc
	return pieceCompletionRef{pc}, nil
}

func (m *Manager) closePieceCompletionLocked(hash string) {
	hash = safeInfoHash(hash)
	if hash == "" || m.tPieceComp == nil {
		return
	}
	if pc, ok := m.tPieceComp[hash]; ok {
		_ = pc.Close()
		delete(m.tPieceComp, hash)
	}
}

func (m *Manager) closeAllPieceCompletionsLocked() {
	for h, pc := range m.tPieceComp {
		if pc != nil {
			_ = pc.Close()
		}
		delete(m.tPieceComp, h)
	}
}

// torrentFileStorage writes torrent data under dir and keeps piece-completion
// under the BDDM data folder, so several torrents can share one download folder
// without locking a database there, and so .torrent.bolt.db is not created in
// the user's folder.
func (m *Manager) torrentFileStorage(dir, hash string) (storage.ClientImpl, error) {
	m.migrateOldPieceCompletionLocked(dir, hash)
	pc, err := m.pieceCompletionLocked(hash)
	if err != nil {
		return nil, err
	}
	return storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   dir,
		PieceCompletion: pc,
		Logger:          slog.New(slog.DiscardHandler),
	}), nil
}

// torrentScratchStorage is used only while looking up magnet metadata. Nothing
// is written to the user's download folder.
func torrentScratchStorage(dir string) storage.ClientImpl {
	return storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   dir,
		PieceCompletion: storage.NewMapPieceCompletion(),
		Logger:          slog.New(slog.DiscardHandler),
	})
}

func (m *Manager) torrentPortPath() string {
	return filepath.Join(m.configDir, "torrent-port")
}

func (m *Manager) readTorrentPort() int {
	b, err := os.ReadFile(m.torrentPortPath())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}

func (m *Manager) writeTorrentPort(port int) {
	if port <= 0 {
		return
	}
	_ = writeFileAtomic(m.torrentPortPath(), []byte(strconv.Itoa(port)+"\n"))
}

func (m *Manager) metaPath(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." {
		return ""
	}
	return filepath.Join(m.torrentsDir(), name)
}

func (m *Manager) saveTorrentMeta(hash string, mi metainfo.MetaInfo) (string, error) {
	if err := os.MkdirAll(m.torrentsDir(), 0755); err != nil {
		return "", err
	}
	name := strings.ToLower(hash) + ".torrent"
	p := filepath.Join(m.torrentsDir(), name)
	f, err := os.Create(p)
	if err != nil {
		return "", err
	}
	err = mi.Write(f)
	cerr := f.Close()
	if err != nil {
		return "", err
	}
	if cerr != nil {
		return "", cerr
	}
	return name, nil
}

func (m *Manager) findTorrentLocked(hash string) *Job {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return nil
	}
	for _, j := range m.jobs {
		if j.Kind == kindTorrent && !j.Removed && j.Torrent != nil && strings.EqualFold(j.Torrent.InfoHash, hash) {
			return j
		}
	}
	return nil
}

func (m *Manager) newTorrentConfig() *torrent.ClientConfig {
	cfg := torrent.NewDefaultClientConfig()
	cfg.Seed = true
	cfg.Debug = false
	cfg.Slogger = slog.New(slog.DiscardHandler)
	cfg.DataDir = filepath.Join(m.configDir, "torrent-work")
	_ = os.MkdirAll(cfg.DataDir, 0755)
	// Do not use storage.NewFile (it would put .torrent.bolt.db in torrent-work).
	// Each started torrent sets its own storage; this is only a fallback.
	cfg.DefaultStorage = torrentScratchStorage(cfg.DataDir)
	cfg.ListenPort = torrentListenPort
	if p := m.readTorrentPort(); p != 0 {
		cfg.ListenPort = p
	}
	if m.torDownLim != nil {
		cfg.DownloadRateLimiter = m.torDownLim
	}
	if m.torUpLim != nil {
		cfg.UploadRateLimiter = m.torUpLim
	}
	if m.tLoopback || testTorrentLoopback {
		m.tLoopback = true
		cfg.ListenHost = func(string) string { return "127.0.0.1" }
		cfg.ListenPort = 0
		cfg.NoDHT = true
		cfg.DisableTrackers = true
		cfg.DisablePEX = true
		cfg.NoDefaultPortForwarding = true
		cfg.DisableIPv6 = true
		cfg.DisableUTP = true
	}
	return cfg
}

// ensureClientLocked starts the shared torrent client. m.mu must be held.
func (m *Manager) ensureClientLocked() error {
	if m.tClient != nil {
		return nil
	}
	if m.closing {
		return errors.New("BDDM is shutting down")
	}
	cfg := m.newTorrentConfig()
	cl, err := torrent.NewClient(cfg)
	if err != nil && cfg.ListenPort != 0 && !m.tLoopback {
		cfg.ListenPort = 0
		cl, err = torrent.NewClient(cfg)
	}
	if err != nil {
		return fmt.Errorf("could not start the torrent engine: %w", err)
	}
	m.tClient = cl
	if p := cl.LocalPort(); p > 0 && !m.tLoopback {
		m.writeTorrentPort(p)
	}
	log.Printf("torrent engine listening on port %d", cl.LocalPort())
	return nil
}

func (m *Manager) closeTorrentClientLocked() {
	if m.tClient != nil {
		_ = m.tClient.Close()
		m.tClient = nil
	}
	m.closeAllPieceCompletionsLocked()
}

func applyTorrentMeta(j *Job, info *metainfo.Info, settings Settings) {
	if info == nil {
		return
	}
	if n := sanitizeName(info.BestName()); n != "" {
		j.Name = n
	}
	j.Size = info.TotalLength()
	files := info.UpvertedFiles()
	if j.Torrent == nil {
		j.Torrent = &TorrentInfo{}
	}
	j.Torrent.Files = len(files)
	j.Torrent.Single = !info.IsDir()
	if j.Dir == "" {
		single := j.Torrent != nil && j.Torrent.Single
		j.Dir = settings.torrentSaveDir(j.Name, single)
	}
}

func torrentFileViews(info *metainfo.Info) []TorrentFileView {
	if info == nil {
		return nil
	}
	out := []TorrentFileView{}
	for _, f := range info.UpvertedFiles() {
		out = append(out, TorrentFileView{Path: f.DisplayPath(info), Size: f.Length})
	}
	return out
}

func magnetURIFromMeta(mi *metainfo.MetaInfo, info *metainfo.Info) string {
	if mi == nil {
		return ""
	}
	m, err := mi.MagnetV2()
	if err == nil {
		return m.String()
	}
	if info == nil {
		inf, e := mi.UnmarshalInfo()
		if e != nil {
			return ""
		}
		info = &inf
	}
	mag := metainfo.Magnet{InfoHash: mi.HashInfoBytes(), DisplayName: info.BestName()}
	return mag.String()
}

// AddTorrentMagnet opens a torrent in the add window (metadata is fetched in
// the background for a magnet link).
func (m *Manager) AddTorrentMagnet(uri string) (JobView, error) {
	p, err := parseMagnet(uri)
	if err != nil {
		return JobView{}, err
	}
	return m.addTorrentPending(p.URI, p.Hash, p.Name, nil)
}

// AddTorrentFile adds a .torrent file (from a path or from its bytes) and
// opens the add window. Metadata is available immediately.
func (m *Manager) AddTorrentFile(path string, data []byte) (JobView, error) {
	var mi metainfo.MetaInfo
	if len(data) > 0 {
		parsed, err := metainfo.Load(bytes.NewReader(data))
		if err != nil {
			return JobView{}, errors.New("This is not a valid .torrent file.")
		}
		mi = *parsed
	} else {
		path = strings.TrimSpace(strings.TrimPrefix(path, "file://"))
		if path == "" {
			return JobView{}, errors.New("Please choose a .torrent file.")
		}
		parsed, err := metainfo.LoadFromFile(path)
		if err != nil {
			return JobView{}, errors.New("This is not a valid .torrent file.")
		}
		mi = *parsed
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return JobView{}, errors.New("This is not a valid .torrent file.")
	}
	hash := strings.ToLower(mi.HashInfoBytes().HexString())
	name := sanitizeName(info.BestName())
	if name == "" {
		name = "Torrent " + hash[:8]
	}
	uri := magnetURIFromMeta(&mi, &info)
	m.mu.Lock()
	if dup := m.findTorrentLocked(hash); dup != nil && dup.Status != StatusPending {
		v := dup.viewLocked()
		m.mu.Unlock()
		return v, errors.New("This torrent is already in the list.")
	}
	metaName, err := m.saveTorrentMeta(hash, mi)
	if err != nil {
		m.mu.Unlock()
		return JobView{}, fmt.Errorf("could not save the torrent: %w", err)
	}
	m.mu.Unlock()
	j, err := m.addTorrentPending(uri, hash, name, &info)
	if err != nil {
		return JobView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.findLocked(j.ID); job != nil && job.Torrent != nil {
		job.Torrent.MetaFile = metaName
		applyTorrentMeta(job, &info, m.settings)
		job.tReady = true
		return job.viewLocked(), nil
	}
	return j, nil
}

func (m *Manager) addTorrentPending(uri, hash, name string, info *metainfo.Info) (JobView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return JobView{}, errors.New("BDDM is shutting down")
	}
	if hash != "" {
		if dup := m.findTorrentLocked(hash); dup != nil && dup.Status != StatusPending {
			return dup.viewLocked(), errors.New("This torrent is already in the list.")
		}
	}
	j := &Job{
		ID: newID(), URL: uri, Name: name, Kind: kindTorrent,
		Status: StatusPending, Started: time.Now(),
		Torrent: &TorrentInfo{InfoHash: hash},
		tSource: "magnet",
	}
	if info != nil {
		j.tSource = "file"
		applyTorrentMeta(j, info, m.settings)
		j.tReady = true
		j.tFiles = torrentFileViews(info)
	} else {
		j.Dir = m.settings.torrentSaveDir(name, false)
	}
	m.jobs = append(m.jobs, j)
	if info == nil && uri != "" {
		go m.resolveMagnet(j.ID, uri)
	}
	return j.viewLocked(), nil
}

func (m *Manager) resolveMagnet(id, uri string) {
	m.mu.Lock()
	if err := m.ensureClientLocked(); err != nil {
		if j := m.findLocked(id); j != nil {
			j.Error = err.Error()
		}
		m.mu.Unlock()
		return
	}
	cl := m.tClient
	j := m.findLocked(id)
	m.mu.Unlock()
	if cl == nil || j == nil {
		return
	}
	spec, err := torrent.TorrentSpecFromMagnetUri(uri)
	if err != nil {
		m.mu.Lock()
		if cur := m.findLocked(id); cur != nil {
			cur.Error = "This is not a valid magnet link."
		}
		m.mu.Unlock()
		return
	}
	spec.Storage = torrentScratchStorage(filepath.Join(m.configDir, "torrent-work"))
	t, _, err := cl.AddTorrentSpec(spec)
	if err != nil {
		m.mu.Lock()
		if cur := m.findLocked(id); cur != nil {
			cur.Error = "Could not add this magnet link: " + err.Error()
		}
		m.mu.Unlock()
		return
	}
	select {
	case <-t.GotInfo():
	case <-time.After(10 * time.Minute):
		t.Drop()
		m.mu.Lock()
		if cur := m.findLocked(id); cur != nil && !cur.tReady {
			cur.Error = "Could not find this torrent's information. Check the magnet link and try again."
		}
		m.mu.Unlock()
		return
	}
	info := t.Info()
	mi := t.Metainfo()
	hash := strings.ToLower(t.InfoHash().HexString())
	metaName, err := m.saveTorrentMeta(hash, mi)
	t.Drop()
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.findLocked(id)
	if cur == nil || cur.Status != StatusPending {
		return
	}
	if err != nil {
		cur.Error = "Could not save the torrent: " + err.Error()
		return
	}
	if cur.Torrent == nil {
		cur.Torrent = &TorrentInfo{}
	}
	cur.Torrent.InfoHash = hash
	cur.Torrent.MetaFile = metaName
	if cur.URL == "" {
		cur.URL = magnetURIFromMeta(&mi, info)
	}
	applyTorrentMeta(cur, info, m.settings)
	cur.tFiles = torrentFileViews(info)
	cur.tReady = true
	cur.Error = ""
}

// TorrentPending is the add-window state for a torrent that is not started yet.
func (m *Manager) TorrentPending(id string) (TorrentPendingView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil || j.Kind != kindTorrent {
		return TorrentPendingView{}, errors.New("torrent not found")
	}
	src := j.tSource
	if src == "" {
		if j.Torrent != nil && j.Torrent.MetaFile != "" {
			src = "file"
		} else {
			src = "magnet"
		}
	}
	v := TorrentPendingView{
		ID: j.ID, Name: j.displayName(), Size: j.Size, Files: j.tFiles,
		Ready: j.tReady, Source: src, URL: j.URL, Dir: j.Dir,
		Error: j.Error, Status: j.Status,
	}
	if v.Dir == "" {
		name := j.Name
		single := j.Torrent != nil && j.Torrent.Single
		v.Dir = m.settings.torrentSaveDir(name, single)
	}
	return v, nil
}

// CancelTorrent drops a torrent that is still in the add window.
func (m *Manager) CancelTorrent(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.findLocked(id)
	if j == nil || j.Kind != kindTorrent {
		return errors.New("torrent not found")
	}
	if j.Status != StatusPending {
		return nil
	}
	if j.th != nil {
		j.th.Drop()
		j.th = nil
	}
	m.removeTorrentMeta(j)
	m.removeTorrentCompletion(j)
	// Remove from the list entirely (it was never confirmed).
	out := m.jobs[:0]
	for _, x := range m.jobs {
		if x != j {
			out = append(out, x)
		}
	}
	m.jobs = out
	return nil
}

// StartTorrent begins downloading every file of a pending torrent into dir.
func (m *Manager) StartTorrent(id, dir string) (JobView, error) {
	m.mu.Lock()
	j := m.findLocked(id)
	if j == nil || j.Kind != kindTorrent {
		m.mu.Unlock()
		return JobView{}, errors.New("torrent not found")
	}
	if j.Status != StatusPending {
		v := j.viewLocked()
		m.mu.Unlock()
		return v, nil
	}
	if !j.tReady && (j.Torrent == nil || j.Torrent.MetaFile == "") {
		err := j.Error
		m.mu.Unlock()
		if err != "" {
			return JobView{}, errors.New(err)
		}
		return JobView{}, errors.New("BDDM is still looking up this torrent. Please wait a moment.")
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = j.Dir
	}
	if dir == "" {
		single := j.Torrent != nil && j.Torrent.Single
		dir = m.settings.torrentSaveDir(j.Name, single)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		m.mu.Unlock()
		return JobView{}, fmt.Errorf("cannot use this folder: %w", err)
	}
	j.Dir = dir
	m.settings.LastTorrentDir = dir
	j.Error = ""
	if err := m.startTorrentLocked(j); err != nil {
		j.Status = StatusError
		j.Error = err.Error()
		m.saveHistoryLocked()
		m.mu.Unlock()
		m.saveSettings()
		return JobView{}, err
	}
	v := j.viewLocked()
	m.saveHistoryLocked()
	m.mu.Unlock()
	m.saveSettings()
	return v, nil
}

// restoreTorrentsLocked re-adds saved torrents that should keep downloading
// or seeding. User-paused torrents stay paused. m.mu must be held.
func (m *Manager) restoreTorrentsLocked() {
	for _, j := range m.jobs {
		if j.Kind != kindTorrent || j.Removed || j.Status == StatusPending || j.Status == StatusError {
			continue
		}
		// User-paused torrents stay paused. Interrupted means BDDM was
		// closed or updated while this torrent was still running.
		if j.Status == StatusPaused && !j.Interrupted {
			continue
		}
		if j.Status == StatusDownloading || j.Status == StatusQueued || j.Status == StatusSeeding || j.Interrupted {
			if err := m.startTorrentLocked(j); err != nil {
				j.Status = StatusError
				j.Error = err.Error()
				log.Printf("[%s] could not restore torrent: %v", j.displayName(), err)
			}
		}
	}
}

func (m *Manager) startTorrentLocked(j *Job) error {
	if j.th != nil {
		return nil
	}
	if err := m.ensureClientLocked(); err != nil {
		return err
	}
	var spec *torrent.TorrentSpec
	if j.Torrent != nil && j.Torrent.MetaFile != "" {
		p := m.metaPath(j.Torrent.MetaFile)
		mi, err := metainfo.LoadFromFile(p)
		if err != nil {
			return fmt.Errorf("could not read the saved torrent: %w", err)
		}
		spec, err = torrent.TorrentSpecFromMetaInfoErr(mi)
		if err != nil {
			return fmt.Errorf("could not read the saved torrent: %w", err)
		}
	} else if j.URL != "" {
		var err error
		spec, err = torrent.TorrentSpecFromMagnetUri(j.URL)
		if err != nil {
			return err
		}
	} else {
		return errors.New("this torrent has no saved data to resume from")
	}
	if j.Dir == "" {
		j.Dir = m.settings.DownloadDir
	}
	if err := os.MkdirAll(j.Dir, 0755); err != nil {
		return fmt.Errorf("cannot use this folder: %w", err)
	}
	hash := ""
	if j.Torrent != nil {
		hash = j.Torrent.InfoHash
	}
	store, err := m.torrentFileStorage(j.Dir, hash)
	if err != nil {
		return err
	}
	spec.Storage = store
	t, _, err := m.tClient.AddTorrentSpec(spec)
	if err != nil {
		return err
	}
	if t.Info() == nil {
		// Magnet resume without a .torrent file yet: wait briefly for metadata.
		m.mu.Unlock()
		select {
		case <-t.GotInfo():
		case <-time.After(2 * time.Minute):
			t.Drop()
			m.mu.Lock()
			return errors.New("could not find this torrent's information after a restart")
		}
		m.mu.Lock()
		if m.closing {
			t.Drop()
			return errors.New("BDDM is shutting down")
		}
	}
	info := t.Info()
	hash = strings.ToLower(t.InfoHash().HexString())
	if j.Torrent == nil {
		j.Torrent = &TorrentInfo{}
	}
	j.Torrent.InfoHash = hash
	if j.Torrent.MetaFile == "" {
		if name, err := m.saveTorrentMeta(hash, t.Metainfo()); err == nil {
			j.Torrent.MetaFile = name
		}
	}
	applyTorrentMeta(j, info, m.settings)
	j.tReady = true
	t.DownloadAll()
	j.th = t
	j.Interrupted = false
	j.Error = ""
	j.stopReason = ""
	if j.Size > 0 && t.BytesCompleted() >= j.Size {
		j.Status = StatusSeeding
		j.done.Store(j.Size)
		if j.Finished.IsZero() {
			j.Finished = time.Now()
		}
	} else {
		j.Status = StatusDownloading
		j.done.Store(t.BytesCompleted())
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	j.finished = make(chan struct{})
	go m.runTorrent(j, ctx)
	return nil
}

func (m *Manager) runTorrent(j *Job, ctx context.Context) {
	defer func() {
		m.mu.Lock()
		if j.th != nil {
			j.th.Drop()
			j.th = nil
		}
		j.cancel = nil
		ch := j.finished
		j.finished = nil
		reason := j.stopReason
		if reason == "pause" && (j.Status == StatusDownloading || j.Status == StatusSeeding) {
			j.Status = StatusPaused
		}
		m.dirty = true
		m.mu.Unlock()
		if ch != nil {
			close(ch)
		}
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	paceTick := time.NewTicker(100 * time.Millisecond)
	defer paceTick.Stop()
	var lastDone, lastUp int64
	lastT := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-paceTick.C:
			m.mu.Lock()
			th := j.th
			m.mu.Unlock()
			if th != nil && th.Info() != nil {
				st := th.Stats()
				m.paceTorrent(j, th.BytesCompleted(), st.BytesWrittenData.Int64())
			}
		case <-tick.C:
			m.mu.Lock()
			th := j.th
			closing := m.closing
			m.mu.Unlock()
			if closing || th == nil {
				return
			}
			if th.Info() == nil {
				continue
			}
			done := th.BytesCompleted()
			st := th.Stats()
			uploaded := st.BytesWrittenData.Int64()
			peers := st.ActivePeers
			now := time.Now()
			el := now.Sub(lastT).Seconds()
			var downSp, upSp float64
			if el > 0.2 {
				downSp = float64(done-lastDone) / el
				upSp = float64(uploaded-lastUp) / el
				if downSp < 0 {
					downSp = 0
				}
				if upSp < 0 {
					upSp = 0
				}
			}
			lastDone, lastUp, lastT = done, uploaded, now
			m.paceTorrent(j, done, uploaded)

			m.mu.Lock()
			if j.th != th {
				m.mu.Unlock()
				return
			}
			j.done.Store(done)
			if length := th.Length(); length > 0 {
				j.Size = length
			}
			if j.Torrent != nil {
				j.Torrent.Uploaded = uploaded
			}
			j.liveMu.Lock()
			j.speed = downSp
			j.upSpeed = upSp
			j.peers = peers
			j.liveMu.Unlock()
			becameSeed := false
			if j.Size > 0 && done >= j.Size && j.Status == StatusDownloading {
				j.Status = StatusSeeding
				if j.Finished.IsZero() {
					j.Finished = time.Now()
					becameSeed = true
				}
			}
			if j.Status == StatusSeeding && j.Size > 0 && done < j.Size {
				// Files went missing after a restart: continue downloading.
				j.Status = StatusDownloading
				j.Finished = time.Time{}
			}
			m.dirty = true
			name := j.displayName()
			m.mu.Unlock()
			if becameSeed {
				log.Printf("[%s] torrent complete; seeding", name)
				if m.notify != nil {
					m.notify("Download finished", name+" is complete and will keep seeding.")
				}
			}
		}
	}
}

func (m *Manager) stopTorrentLocked(j *Job, reason string) {
	if j.Kind != kindTorrent {
		return
	}
	j.stopReason = reason
	if j.cancel != nil {
		j.cancel()
	}
}

func (m *Manager) removeTorrentFiles(j *Job) {
	if j.Dir == "" || j.Name == "" {
		return
	}
	full := filepath.Join(j.Dir, j.Name)
	if filepath.Base(full) != j.Name {
		return
	}
	_ = os.RemoveAll(full)
	_ = os.RemoveAll(full + ".part")
}

func (m *Manager) removeTorrentMeta(j *Job) {
	if j == nil || j.Torrent == nil || j.Torrent.MetaFile == "" {
		return
	}
	if p := m.metaPath(j.Torrent.MetaFile); p != "" {
		_ = os.Remove(p)
	}
}

func (m *Manager) removeTorrentCompletion(j *Job) {
	if j == nil || j.Torrent == nil {
		return
	}
	hash := safeInfoHash(j.Torrent.InfoHash)
	if hash == "" {
		return
	}
	m.closePieceCompletionLocked(hash)
	if dir := m.pieceCompletionDir(hash); dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// maybeRemoveOldCompletionInDirLocked deletes a leftover .torrent.bolt.db from
// the user's folder when no remaining torrent still uses that folder.
func (m *Manager) maybeRemoveOldCompletionInDirLocked(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	want := filepath.Clean(dir)
	for _, j := range m.jobs {
		if j == nil || j.Kind != kindTorrent || j.Removed {
			continue
		}
		if j.Dir != "" && filepath.Clean(j.Dir) == want {
			return
		}
	}
	_ = os.Remove(filepath.Join(dir, torrentPieceDBName))
}

// addTorrentClientPeer is used by tests to introduce a local seeder.
func (m *Manager) addTorrentClientPeer(id string, cl *torrent.Client) error {
	if cl == nil {
		return errors.New("no peer")
	}
	m.mu.Lock()
	j := m.findLocked(id)
	th := (*torrent.Torrent)(nil)
	if j != nil {
		th = j.th
	}
	m.mu.Unlock()
	if th == nil {
		return errors.New("torrent is not running")
	}
	if th.AddClientPeer(cl) == 0 {
		return errors.New("could not add the test peer")
	}
	return nil
}

func torrentDataPath(j *Job) (dir, file string) {
	if j == nil {
		return "", ""
	}
	if j.Name == "" {
		return j.Dir, ""
	}
	full := filepath.Join(j.Dir, j.Name)
	if fileExists(full) {
		return j.Dir, full
	}
	return j.Dir, ""
}

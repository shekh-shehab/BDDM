package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseDriveLink(t *testing.T) {
	const id = "1l_5RK28JRL19wpT22B-DY9We3TVXnnQQ"
	cases := []struct {
		in, kind, id, doc, rk string
	}{
		{"https://drive.google.com/file/d/" + id + "/view?usp=sharing", "file", id, "", ""},
		{"https://drive.google.com/file/u/1/d/" + id + "/view", "file", id, "", ""},
		{"https://drive.google.com/open?id=" + id, "file", id, "", ""},
		{"https://drive.google.com/uc?id=" + id + "&export=download", "file", id, "", ""},
		{"https://drive.usercontent.google.com/download?id=" + id + "&export=download&confirm=t&uuid=x", "file", id, "", ""},
		{"https://docs.google.com/uc?export=download&id=" + id, "file", id, "", ""},
		{"https://drive.google.com/drive/folders/" + id + "?usp=sharing", "folder", id, "", ""},
		{"https://drive.google.com/drive/u/0/folders/" + id + "?resourcekey=0-abc", "folder", id, "", "0-abc"},
		{"https://drive.google.com/drive/mobile/folders/" + id, "folder", id, "", ""},
		{"https://docs.google.com/document/d/" + id + "/edit?usp=sharing", "doc", id, "document", ""},
		{"https://docs.google.com/spreadsheets/d/" + id + "/edit#gid=0", "doc", id, "spreadsheets", ""},
		{"https://docs.google.com/presentation/u/0/d/" + id + "/edit", "doc", id, "presentation", ""},
	}
	for _, c := range cases {
		l, ok := parseDriveLink(c.in)
		if !ok || l.Kind != c.kind || l.ID != c.id || l.DocType != c.doc || l.ResourceKey != c.rk {
			t.Errorf("%s: got %+v ok=%v", c.in, l, ok)
		}
	}
	for _, bad := range []string{
		"https://example.com/file/d/" + id + "/view",
		"https://drive.google.com/",
		"https://docs.google.com/document/d/" + id + "/export?format=pdf",
		"ftp://drive.google.com/file/d/" + id,
		"not a link",
	} {
		if l, ok := parseDriveLink(bad); ok {
			t.Errorf("%s: should not be a Drive link, got %+v", bad, l)
		}
	}
}

func TestDriveKeyAndExt(t *testing.T) {
	a := driveKey("https://drive.google.com/file/d/ABCDEFGHIJKL/view")
	b := driveKey("https://drive.usercontent.google.com/download?id=ABCDEFGHIJKL&export=download")
	if a == "" || a != b {
		t.Fatalf("same file, different keys: %q %q", a, b)
	}
	if driveKey("https://example.com/x.zip") != "" {
		t.Fatal("non-Drive link got a key")
	}
	if got := withExt("Report", "pdf"); got != "Report.pdf" {
		t.Fatal(got)
	}
	if got := withExt("Report.pdf", "docx"); got != "Report.docx" {
		t.Fatal(got)
	}
	if f := docFormats("spreadsheets"); len(f) != 3 || f[0] != "xlsx" {
		t.Fatal(f)
	}
	if !validDocFormat("presentation", "pptx") || validDocFormat("presentation", "csv") {
		t.Fatal("validDocFormat")
	}
}

// The virus-scan warning page as Google sends it (Nov 2026).
const scanPage = `<!DOCTYPE html><html><head><title>Google Drive - Virus scan warning</title></head><body>
<div class="uc-main"><p class="uc-warning-subcaption">Google Drive can't scan this file for viruses.</p>
<form id="download-form" action="https://drive.usercontent.google.com/download" method="get"><input type="submit" id="uc-download-link" class="goog-inline-block jfk-button jfk-button-action" value="Download anyway"/><input type="hidden" name="id" value="1l_5RK28JRL19wpT22B-DY9We3TVXnnQQ"><input type="hidden" name="export" value="download"><input type="hidden" name="confirm" value="t"><input type="hidden" name="uuid" value="319d30ea-38fe-4b3f-ad7c-49f22f3babab"><input type="hidden" name="at" value="AN8x&amp;y"></form></div></body></html>`

func TestParseDriveConfirm(t *testing.T) {
	got := parseDriveConfirm(scanPage, "https://drive.usercontent.google.com/download?id=x")
	u, err := url.Parse(got)
	if err != nil || u.Host != "drive.usercontent.google.com" || u.Path != "/download" {
		t.Fatalf("bad link %q", got)
	}
	q := u.Query()
	if q.Get("id") != "1l_5RK28JRL19wpT22B-DY9We3TVXnnQQ" || q.Get("confirm") != "t" ||
		q.Get("uuid") != "319d30ea-38fe-4b3f-ad7c-49f22f3babab" || q.Get("export") != "download" || q.Get("at") != "AN8x&y" {
		t.Fatalf("bad query %v", q)
	}
	if strings.Contains(got, "Download") {
		t.Fatalf("submit button leaked into link: %q", got)
	}
	old := `<a id="uc-download-link" href="/uc?export=download&amp;confirm=AbCd&amp;id=XYZXYZXYZXYZ">Download anyway</a>`
	if got := parseDriveConfirm(old, ""); got != "https://drive.google.com/uc?export=download&confirm=AbCd&id=XYZXYZXYZXYZ" {
		t.Fatalf("old page: %q", got)
	}
	if parseDriveConfirm("<html>nothing</html>", "") != "" {
		t.Fatal("found a link in an empty page")
	}
}

func TestDriveProblem(t *testing.T) {
	if err := driveProblem("Sorry, you can't view or download this file at this time. Too many users have viewed or downloaded this file recently."); err == nil || err.Error() != msgDriveQuota {
		t.Fatalf("quota: %v", err)
	}
	if err := driveProblem("You need access. Request access, or switch to an account with access."); err == nil || err.Error() != msgDrivePrivate {
		t.Fatalf("private: %v", err)
	}
	if !isDriveFinal(driveProblem("Too many users have viewed or downloaded this file recently")) {
		t.Fatal("quota error must stop retries")
	}
}

func jsEscape(s string) string {
	r := strings.NewReplacer(`"`, `\x22`, `[`, `\x5b`, `]`, `\x5d`, `/`, `\/`)
	return r.Replace(s)
}

func TestParseFolderPage(t *testing.T) {
	const root = "ROOTFOLDER123"
	rows := `[[` +
		`["SUBFOLDER0001",["ROOTFOLDER123"],"directory-0","application/vnd.google-apps.folder",0,null,0,0,0,1,1,null,null,null],` +
		`["FILE000000001",["ROOTFOLDER123"],"a \u00e9 b.txt","text/plain",0,null,0,0,0,1,1,null,null,1234],` +
		`["DOC0000000001",["ROOTFOLDER123"],"Notes","application/vnd.google-apps.document",0,null,0,0,0,1,1,null,null,null],` +
		`["SHORT00000001",["ROOTFOLDER123"],"Link to x","application/vnd.google-apps.shortcut",0,null,0,0,0,1,1,null,null,null],` +
		`["FORM000000001",["ROOTFOLDER123"],"Survey","application/vnd.google-apps.form",0,null,0,0,0,1,1,null,null,null],` +
		`["OTHER00000001",["SOMEWHEREELSE"],"stranger.bin","application/octet-stream",0,null,0,0,0,1,1,null,null,99]` +
		`],null]`
	page := `<script>window['_DRIVE_ivd'] = '` + jsEscape(rows) + `';</script>`
	items, foreign, ok := parseFolderPage(page, root)
	if !ok {
		t.Fatal("not parsed")
	}
	if foreign != 1 || len(items) != 5 {
		t.Fatalf("foreign=%d items=%d", foreign, len(items))
	}
	by := map[string]*DriveNode{}
	for _, n := range items {
		by[n.ID] = n
	}
	if !by["SUBFOLDER0001"].Folder {
		t.Error("folder not marked")
	}
	if f := by["FILE000000001"]; f.Size != 1234 || f.Name != "a é b.txt" {
		t.Errorf("file: %+v", f)
	}
	if d := by["DOC0000000001"]; d.DocType != "document" || d.Export != "docx" {
		t.Errorf("doc: %+v", d)
	}
	if !by["SHORT00000001"].Shortcut {
		t.Error("shortcut not marked")
	}
	if by["FORM000000001"].Skip == "" {
		t.Error("form should be skipped")
	}
	if _, _, ok := parseFolderPage("<html></html>", root); ok {
		t.Error("parsed a page without data")
	}
}

func TestJSUnescape(t *testing.T) {
	if got := jsUnescape(`\x5b\x22a\/b\x22\x5d \u00e9\n`); got != "[\"a/b\"] é\n" {
		t.Fatalf("%q", got)
	}
}

func TestParseEmbeddedFolder(t *testing.T) {
	page := `<html><head><title>gdown_folder_test</title></head><body>
<div class="flip-entry" id="entry-1aMZqPaU03E7XOQNXtjSCdguRHBaIQ82m" tabindex="0" role="link"><div class="flip-entry-info"><a href="https://drive.google.com/drive/folders/1aMZqPaU03E7XOQNXtjSCdguRHBaIQ82m" target="_blank"><div class="flip-entry-title">directory-0</div></a></div></div>
<div class="flip-entry" id="entry-1Ab2Cd3Ef4Gh5Ij6Kl" tabindex="0" role="link"><div class="flip-entry-info"><a href="https://drive.google.com/file/d/1Ab2Cd3Ef4Gh5Ij6Kl/view?usp=drive_web" target="_blank"><div class="flip-entry-title">Tom &amp; Jerry.mp4</div></a></div></div>
<div class="flip-entry" id="entry-1DocDocDocDocDoc" tabindex="0" role="link"><div class="flip-entry-info"><a href="https://docs.google.com/spreadsheets/d/1DocDocDocDocDoc/edit?usp=drive_web" target="_blank"><div class="flip-entry-title">Budget</div></a></div></div>
</body></html>`
	title, items := parseEmbeddedFolder(page)
	if title != "gdown_folder_test" || len(items) != 3 {
		t.Fatalf("title=%q items=%d", title, len(items))
	}
	if !items[0].Folder || items[1].Name != "Tom & Jerry.mp4" || items[1].Folder || items[2].DocType != "spreadsheets" || items[2].Export != "xlsx" {
		t.Fatalf("%+v %+v %+v", items[0], items[1], items[2])
	}
}

// A server that sends no size (chunked, like Google Docs exports) must give a
// finished file, not a download that keeps fetching the same bytes.
func TestUnknownSizeFinishes(t *testing.T) {
	data := randomData(t, 300_000)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		fl := w.(http.Flusher)
		for i := 0; i < len(data); i += 50_000 {
			w.Write(data[i:min(i+50_000, len(data))])
			fl.Flush() // no Content-Length: chunked answer
		}
	}))
	defer srv.Close()
	m, dl := newTestManager(t, 8)
	j, err := m.Add(AddRequest{URL: srv.URL + "/export.bin"})
	if err != nil {
		t.Fatal(err)
	}
	v := waitStatus(t, m, j.ID, StatusDone, 15*time.Second)
	checkFile(t, filepath.Join(dl, "export.bin"), data)
	if v.Size != int64(len(data)) {
		t.Errorf("size %d, want %d", v.Size, len(data))
	}
	if h := hits.Load(); h > 3 { // HEAD + GET probe + the download
		t.Errorf("server was asked %d times", h)
	}
}

func TestDriveAuthUserAndPages(t *testing.T) {
	id := "1l_5RK28JRL19wpT22B-DY9We3TVXnnQQ"
	cases := map[string]string{
		"https://drive.google.com/file/u/1/d/" + id + "/view":                                    "1",
		"https://drive.google.com/file/d/" + id + "/view?authuser=2":                             "2",
		"https://drive.usercontent.google.com/download?id=" + id + "&export=download&authuser=1": "1",
		"https://drive.usercontent.google.com/u/1/uc?id=" + id + "&export=download":              "1",
		"https://drive.google.com/drive/u/3/folders/" + id:                                       "3",
		"https://docs.google.com/document/u/1/d/" + id + "/edit":                                 "1",
		"https://drive.google.com/file/d/" + id + "/view?usp=sharing":                            "",
	}
	for raw, want := range cases {
		l, ok := parseDriveLink(raw)
		if !ok || l.AuthUser != want {
			t.Errorf("%s: ok=%v authuser=%q want %q", raw, ok, l.AuthUser, want)
		}
	}
	l, _ := parseDriveLink("https://drive.google.com/file/u/1/d/" + id + "/view?resourcekey=0-x")
	if got := driveDownloadURL(l); got != "https://drive.usercontent.google.com/download?id="+id+"&export=download&resourcekey=0-x&authuser=1" {
		t.Errorf("download url %s", got)
	}
	pages := []string{
		"https://drive.google.com/drive/u/1/quota",
		"https://drive.google.com/drive/my-drive",
		"https://drive.google.com/drive/u/0/home",
		"https://drive.google.com/drive/recent",
		"https://drive.google.com/",
		"https://docs.google.com/document/u/0/",
	}
	for _, p := range pages {
		if !driveNonFilePage(p) {
			t.Errorf("%s should be a Drive page, not a file", p)
		}
	}
	notPages := []string{
		"https://drive.google.com/file/d/" + id + "/view",
		"https://drive.google.com/drive/u/1/folders/" + id,
		"https://docs.google.com/document/d/" + id + "/export?format=pdf",
		"https://example.com/drive/quota",
		"https://drive.usercontent.google.com/download?id=" + id,
	}
	for _, p := range notPages {
		if driveNonFilePage(p) {
			t.Errorf("%s must not be refused", p)
		}
	}
}

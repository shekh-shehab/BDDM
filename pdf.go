package main

// pdf.go: "Save as PDF" for view-only Google Drive files. The owner turned
// off downloading, so the browser extension captures the page images that
// Drive's viewer shows and sends them here one by one; BDDM writes them into
// one PDF in the downloads folder and lists it as a finished download.
//
//	POST /ext/pdf/start          {"name","url","pages"}  -> {"id"}
//	POST /ext/pdf/{id}/page      {"n","data"}  (data: a data: URL, PNG or JPEG)
//	POST /ext/pdf/{id}/finish    {"name","format","pageSize","orientation","allowMissing"}
//	POST /ext/pdf/{id}/cancel
//
// format: "pdf" (one PDF), "png" (a folder of PNG images) or "zip" (a ZIP of
// PNG images). pageSize: "fit" (each page as big as its image), "a3", "a4",
// "a5", "letter", "legal", "tabloid"; orientation: "portrait"/"landscape".

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pdfMaxPages    = 2000
	pdfMaxPageLen  = 40 << 20 // one page image
	pdfSessionTTL  = 2 * time.Hour
	kindPDFCapture = "pdfcapture"
)

type pdfSession struct {
	id      string
	name    string // file name, ends in .pdf
	url     string
	pages   int // pages the viewer has (0 = unknown)
	dir     string
	got     map[int]string // page number (0-based) -> image file (as sent)
	touched time.Time
}

var pdfSessions = struct {
	sync.Mutex
	m map[string]*pdfSession
}{m: map[string]*pdfSession{}}

// pdfFileName makes a safe "<name>.pdf" from the Drive file name.
func pdfFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, " - Google Drive")
	switch strings.ToLower(filepath.Ext(name)) { // "Notes.docx" -> "Notes.pdf"
	case ".pdf", ".doc", ".docx", ".ppt", ".pptx", ".odt", ".odp", ".rtf", ".txt", ".png", ".zip":
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	name = sanitizeName(strings.TrimSpace(name))
	if name == "" || name == "download" {
		name = "Google Drive document"
	}
	return name + ".pdf"
}

func (m *Manager) pdfTempRoot() string { return filepath.Join(m.configDir, "pdf-capture") }

// PDFStart begins a capture.
func (m *Manager) PDFStart(name, rawURL string, pages int) (string, error) {
	if pages < 0 || pages > pdfMaxPages {
		return "", errors.New("too many pages")
	}
	pdfSessions.Lock()
	defer pdfSessions.Unlock()
	for id, s := range pdfSessions.m { // forget captures that were left behind
		if time.Since(s.touched) > pdfSessionTTL {
			os.RemoveAll(s.dir)
			delete(pdfSessions.m, id)
		}
	}
	id := newID()
	dir := filepath.Join(m.pdfTempRoot(), id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	pdfSessions.m[id] = &pdfSession{id: id, name: pdfFileName(name), url: rawURL, pages: pages,
		dir: dir, got: map[int]string{}, touched: time.Now()}
	return id, nil
}

func getPDFSession(id string) (*pdfSession, error) {
	s := pdfSessions.m[id]
	if s == nil {
		return nil, errors.New("this capture is not known (BDDM was restarted?). Please try again")
	}
	s.touched = time.Now()
	return s, nil
}

// decodeDataURL reads "data:image/...;base64,...." (or plain base64).
func decodeDataURL(s string) ([]byte, error) {
	if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i > 0 {
		s = s[i+1:]
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}

// PDFPage stores page n (0-based).
func (m *Manager) PDFPage(id string, n int, dataURL string) error {
	data, err := decodeDataURL(dataURL)
	if err != nil || len(data) == 0 {
		return errors.New("the page image was empty or damaged")
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return errors.New("the page image could not be read")
	}
	pdfSessions.Lock()
	defer pdfSessions.Unlock()
	s, err := getPDFSession(id)
	if err != nil {
		return err
	}
	if n < 0 || n >= pdfMaxPages || (s.pages > 0 && n >= s.pages) {
		return fmt.Errorf("page %d is out of range", n+1)
	}
	f := filepath.Join(s.dir, fmt.Sprintf("%05d.%s", n, map[string]string{"png": "png", "jpeg": "jpg"}[format]))
	if err := os.WriteFile(f, data, 0644); err != nil {
		return err
	}
	s.got[n] = f
	return nil
}

// PDFCancel throws a capture away.
func (m *Manager) PDFCancel(id string) {
	pdfSessions.Lock()
	defer pdfSessions.Unlock()
	if s := pdfSessions.m[id]; s != nil {
		os.RemoveAll(s.dir)
		delete(pdfSessions.m, id)
	}
}

// PDFOptions are the choices from the extension's Save as PDF panel.
type PDFOptions struct {
	Name         string `json:"name"`
	Format       string `json:"format"`      // pdf, png, zip
	PageSize     string `json:"pageSize"`    // fit, a3, a4, a5, letter, legal, tabloid
	Orientation  string `json:"orientation"` // portrait, landscape
	AllowMissing bool   `json:"allowMissing"`
}

// Page sizes in points (1/72 inch), portrait.
var pdfPageSizes = map[string][2]float64{
	"a3": {841.89, 1190.55}, "a4": {595.28, 841.89}, "a5": {419.53, 595.28},
	"letter": {612, 792}, "legal": {612, 1008}, "tabloid": {792, 1224},
}

// PDFFinish writes the result into the downloads folder and lists it as a
// finished download.
func (m *Manager) PDFFinish(id string, o PDFOptions) (JobView, int, error) {
	o.Format = strings.ToLower(strings.TrimSpace(o.Format))
	if o.Format == "" {
		o.Format = "pdf"
	}
	if o.Format != "pdf" && o.Format != "png" && o.Format != "zip" {
		return JobView{}, 0, errors.New("unknown format")
	}
	o.PageSize = strings.ToLower(strings.TrimSpace(o.PageSize))
	if _, ok := pdfPageSizes[o.PageSize]; !ok {
		o.PageSize = "fit"
	}
	pdfSessions.Lock()
	s, err := getPDFSession(id)
	if err != nil {
		pdfSessions.Unlock()
		return JobView{}, 0, err
	}
	nums := make([]int, 0, len(s.got))
	for n := range s.got {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	missing := 0
	for i := 0; i < s.pages; i++ {
		if _, ok := s.got[i]; !ok {
			missing++
		}
	}
	files := make([]string, len(nums))
	for i, n := range nums {
		files[i] = s.got[n]
	}
	base := s.name
	if strings.TrimSpace(o.Name) != "" {
		base = pdfFileName(o.Name)
	}
	rawURL := s.url
	pdfSessions.Unlock()
	if len(files) == 0 {
		return JobView{}, 0, errors.New("no pages were captured")
	}
	if missing > 0 && !o.AllowMissing {
		return JobView{}, 0, fmt.Errorf("%d page(s) are missing", missing)
	}
	stem := strings.TrimSuffix(base, ".pdf")

	m.mu.Lock()
	dir := m.settings.dirFor(base) // the Documents folder
	if o.Format != "pdf" {
		dir = m.settings.dirFor("x.png")
	}
	m.mu.Unlock()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return JobView{}, 0, err
	}
	var name string
	var size int64
	switch o.Format {
	case "png": // a folder "<name>" with "<name> - page 001.png", ...
		name, err = createFree(dir, stem, "", func(p string) error { return os.Mkdir(p, 0755) })
		if err == nil {
			out := filepath.Join(dir, name)
			err = writePNGs(len(files), files, func(i int) (io.WriteCloser, error) {
				return os.Create(filepath.Join(out, pngPageName(stem, i, len(files))))
			})
			if err != nil {
				os.RemoveAll(out)
			} else if list, e := os.ReadDir(out); e == nil {
				for _, d := range list {
					if fi, e := d.Info(); e == nil {
						size += fi.Size()
					}
				}
			}
		}
	default:
		ext := ".pdf"
		if o.Format == "zip" {
			ext = ".zip"
		}
		var f *os.File
		name, err = createFree(dir, stem, ext, func(p string) (e error) {
			f, e = os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			return e
		})
		if err == nil {
			if o.Format == "zip" {
				err = writePNGZip(f, stem, files)
			} else {
				err = writeImagePDF(f, files, stem, o.PageSize, o.Orientation == "landscape")
			}
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(filepath.Join(dir, name))
			}
		}
		if fi, e := os.Stat(filepath.Join(dir, name)); err == nil && e == nil {
			size = fi.Size()
		}
	}
	if err != nil {
		return JobView{}, 0, err
	}
	m.PDFCancel(id) // the temporary page images

	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	j := &Job{ID: newID(), URL: rawURL, Name: name, Dir: dir, Size: size, Status: StatusDone,
		Kind: kindPDFCapture, Started: now, Finished: now, Parts: 1}
	j.done.Store(size)
	m.jobs = append(m.jobs, j)
	m.saveHistoryLocked()
	body := fmt.Sprintf("%s (%d pages)", name, len(files))
	if missing > 0 {
		body = fmt.Sprintf("%s (%d pages, %d missing)", name, len(files), missing)
	}
	title := map[string]string{"pdf": "Saved as PDF", "png": "Saved as PNG images", "zip": "Saved as ZIP of PNG images"}[o.Format]
	m.announceLocked(title, body, "done")
	return j.viewLocked(), len(files), nil
}

// createFree creates stem+ext (or "stem (1)"+ext, ...) without replacing
// anything that is already there, and returns the name it used.
func createFree(dir, stem, ext string, create func(path string) error) (string, error) {
	for i := 0; i < 1000; i++ {
		name := numberedName(stem+ext, i)
		if ext == "" && i > 0 {
			name = fmt.Sprintf("%s (%d)", stem, i)
		}
		err := create(filepath.Join(dir, name))
		if err == nil {
			return name, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	return "", errors.New("could not find a free file name")
}

func pngPageName(stem string, i, total int) string {
	w := len(fmt.Sprint(total))
	if w < 3 {
		w = 3
	}
	return fmt.Sprintf("%s - page %0*d.png", stem, w, i+1)
}

// pagePNG returns the page as PNG bytes.
func pagePNG(file string) ([]byte, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(file, ".png") {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("a page image could not be read")
	}
	var buf bytes.Buffer
	err = png.Encode(&buf, img)
	return buf.Bytes(), err
}

func writePNGs(total int, files []string, open func(i int) (io.WriteCloser, error)) error {
	for i, file := range files {
		data, err := pagePNG(file)
		if err != nil {
			return err
		}
		w, err := open(i)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writePNGZip(w io.Writer, stem string, files []string) error {
	z := zip.NewWriter(w)
	for i, file := range files {
		data, err := pagePNG(file)
		if err != nil {
			return err
		}
		// PNG is already compressed: store it.
		fw, err := z.CreateHeader(&zip.FileHeader{Name: pngPageName(stem, i, len(files)), Method: zip.Store, Modified: time.Now()})
		if err != nil {
			return err
		}
		if _, err := fw.Write(data); err != nil {
			return err
		}
	}
	return z.Close()
}

// pdfImage is one page image ready for the PDF: JPEG (DCTDecode) or raw RGB
// compressed with zlib (FlateDecode, lossless), whichever is smaller.
type pdfImage struct {
	w, h   int
	cs     string
	filter string
	decode string
	data   []byte
}

func loadPDFImage(file string) (pdfImage, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return pdfImage{}, err
	}
	if strings.HasSuffix(file, ".jpg") {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return pdfImage{}, errors.New("a page image could not be read")
		}
		p := pdfImage{w: cfg.Width, h: cfg.Height, cs: "/DeviceRGB", filter: "/DCTDecode", data: data}
		switch cfg.ColorModel {
		case color.GrayModel:
			p.cs = "/DeviceGray"
		case color.CMYKModel:
			p.cs = "/DeviceCMYK"
			p.decode = " /Decode [1 0 1 0 1 0 1 0]" // Adobe JPEGs store CMYK inverted
		}
		return p, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return pdfImage{}, errors.New("a page image could not be read")
	}
	b := img.Bounds()
	// Transparent parts become white (as on paper).
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Over)

	// Lossless (good for text pages): PNG's compressed data can go into the
	// PDF as it is (FlateDecode with the PNG predictor). Gray pages are stored
	// as gray, a third of the size.
	var src image.Image = rgba
	gray := true
	for i := 0; i < len(rgba.Pix) && gray; i += 4 {
		gray = rgba.Pix[i] == rgba.Pix[i+1] && rgba.Pix[i+1] == rgba.Pix[i+2]
	}
	if gray {
		g := image.NewGray(rgba.Bounds())
		for i, j := 0, 0; i < len(rgba.Pix); i, j = i+4, j+1 {
			g.Pix[j] = rgba.Pix[i]
		}
		src = g
	}
	var pb bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&pb, src); err != nil {
		return pdfImage{}, err
	}
	idat, colors, err := pngIDAT(pb.Bytes())
	if err != nil {
		return pdfImage{}, err
	}
	cs := "/DeviceRGB"
	if colors == 1 {
		cs = "/DeviceGray"
	}
	flate := pdfImage{w: b.Dx(), h: b.Dy(), cs: cs, filter: "/FlateDecode",
		decode: fmt.Sprintf(" /DecodeParms << /Predictor 15 /Colors %d /BitsPerComponent 8 /Columns %d >>", colors, b.Dx()), data: idat}
	// ... unless JPEG is much smaller (photos, scans).
	var jp bytes.Buffer
	if err := jpeg.Encode(&jp, src, &jpeg.Options{Quality: 92}); err == nil && jp.Len()*2 < len(idat) {
		jcs := "/DeviceRGB"
		if gray {
			jcs = "/DeviceGray"
		}
		return pdfImage{w: b.Dx(), h: b.Dy(), cs: jcs, filter: "/DCTDecode", data: jp.Bytes()}, nil
	}
	return flate, nil
}

// pngIDAT returns the compressed image data of a PNG written by Go's encoder
// (8 bits, not interlaced) and its number of color channels.
func pngIDAT(data []byte) ([]byte, int, error) {
	bad := errors.New("unexpected PNG data")
	if len(data) < 8 || string(data[1:4]) != "PNG" {
		return nil, 0, bad
	}
	var out []byte
	colors := 0
	for p := 8; p+8 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[p:]))
		typ := string(data[p+4 : p+8])
		if p+12+n > len(data) {
			return nil, 0, bad
		}
		body := data[p+8 : p+8+n]
		switch typ {
		case "IHDR":
			if n < 13 || body[8] != 8 || body[12] != 0 {
				return nil, 0, bad
			}
			switch body[9] {
			case 0:
				colors = 1
			case 2:
				colors = 3
			default:
				return nil, 0, bad
			}
		case "IDAT":
			out = append(out, body...)
		}
		p += 12 + n
	}
	if colors == 0 || len(out) == 0 {
		return nil, 0, bad
	}
	return out, colors, nil
}

// writeImagePDF writes a PDF with one image per page. pageSize "fit": each
// page has its image's aspect ratio, 595 points (A4) wide. Otherwise every
// page has that paper size and the image is fitted in the middle.
func writeImagePDF(w io.Writer, files []string, title, pageSize string, landscape bool) error {
	cw := &countWriter{w: w}
	offs := []int64{0} // object numbers start at 1
	newObj := func() int { offs = append(offs, 0); return len(offs) - 1 }
	begin := func(n int) { offs[n] = cw.n; fmt.Fprintf(cw, "%d 0 obj\n", n) }

	fmt.Fprint(cw, "%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	catalog, pagesObj, info := newObj(), newObj(), newObj()
	kids := []int{}
	// Prepare the pages on all CPUs (compressing is the slow part), a few
	// pages ahead of the writer so memory stays small.
	type prepared struct {
		img pdfImage
		err error
	}
	results := make([]chan prepared, len(files))
	for i := range results {
		results[i] = make(chan prepared, 1)
	}
	next := 0
	startNext := func() {
		if next < len(files) {
			i := next
			next++
			go func() {
				img, err := loadPDFImage(files[i])
				results[i] <- prepared{img, err} // buffered: never blocks
			}()
		}
	}
	for k := 0; k < max(1, min(runtime.NumCPU(), 8))*2; k++ {
		startNext()
	}
	for i := range files {
		r := <-results[i]
		startNext()
		img, err := r.img, r.err
		if err != nil {
			return err
		}
		var pw, ph, x, y, dw, dh float64
		if sz, ok := pdfPageSizes[pageSize]; ok {
			pw, ph = sz[0], sz[1]
			if landscape {
				pw, ph = ph, pw
			}
			scale := min(pw/float64(img.w), ph/float64(img.h))
			dw, dh = float64(img.w)*scale, float64(img.h)*scale
			x, y = (pw-dw)/2, (ph-dh)/2
		} else {
			pw = 595.28
			ph = pw * float64(img.h) / float64(img.w)
			dw, dh = pw, ph
		}
		page, content, im := newObj(), newObj(), newObj()
		kids = append(kids, page)

		begin(im)
		fmt.Fprintf(cw, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8%s /Filter %s /Length %d >>\nstream\n",
			img.w, img.h, img.cs, img.decode, img.filter, len(img.data))
		cw.Write(img.data)
		fmt.Fprint(cw, "\nendstream\nendobj\n")

		stream := fmt.Sprintf("q %.2f 0 0 %.2f %.2f %.2f cm /Im0 Do Q\n", dw, dh, x, y)
		begin(content)
		fmt.Fprintf(cw, "<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(stream), stream)

		begin(page)
		fmt.Fprintf(cw, "<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>\nendobj\n",
			pagesObj, pw, ph, im, content)
		if cw.err != nil {
			return cw.err
		}
	}
	begin(pagesObj)
	refs := make([]string, len(kids))
	for i, k := range kids {
		refs[i] = fmt.Sprintf("%d 0 R", k)
	}
	fmt.Fprintf(cw, "<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", strings.Join(refs, " "), len(kids))
	begin(catalog)
	fmt.Fprintf(cw, "<< /Type /Catalog /Pages %d 0 R >>\nendobj\n", pagesObj)
	begin(info)
	fmt.Fprintf(cw, "<< /Title %s /Producer (BDDM) /CreationDate (D:%s) >>\nendobj\n",
		pdfText(title), time.Now().Format("20060102150405-07'00'"))

	xref := cw.n
	fmt.Fprintf(cw, "xref\n0 %d\n0000000000 65535 f \n", len(offs))
	for _, o := range offs[1:] {
		fmt.Fprintf(cw, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(cw, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs), catalog, info, xref)
	return cw.err
}

// pdfText writes a PDF text string; non-ASCII names are written as UTF-16.
func pdfText(s string) string {
	ascii := true
	for _, r := range s {
		if r > 126 || r < 32 {
			ascii = false
			break
		}
	}
	if ascii {
		r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
		return "(" + r.Replace(s) + ")"
	}
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			fmt.Fprintf(&b, "%04X%04X", 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		} else {
			fmt.Fprintf(&b, "%04X", r)
		}
	}
	b.WriteString(">")
	return b.String()
}

type countWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *countWriter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.err = err
	return n, err
}

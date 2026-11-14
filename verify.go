package main

// verify.go: checking a finished download against a checksum (A7).
//
// The checksum comes from the user (the "Checksum" box: MD5, SHA-1, SHA-256
// or SHA-512, optionally written as "sha256:..."), or from the server's
// headers (Repr-Digest, Digest, Content-MD5, x-goog-hash, X-Checksum-*).
// When the file does not match, BDDM downloads it again piece by piece,
// compares every piece with the file and rewrites only the damaged ones.
// When the server sends a checksum for each piece (Content-MD5 or
// Content-Digest on a range answer), each piece is checked as it arrives and
// a damaged one is downloaded again at once.

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// Checksum is an expected hash: Algo is md5, sha1, sha256, sha512 or crc32c;
// Hex is lower-case hex.
type Checksum struct {
	Algo string `json:"algo"`
	Hex  string `json:"hex"`
	From string `json:"from,omitempty"` // "user" or the header it came from
}

func (c *Checksum) String() string {
	if c == nil {
		return ""
	}
	return c.Algo + ":" + c.Hex
}

// algoName is how the algorithm is shown to the user.
func algoName(a string) string {
	switch a {
	case "md5":
		return "MD5"
	case "sha1":
		return "SHA-1"
	case "sha256":
		return "SHA-256"
	case "sha512":
		return "SHA-512"
	case "crc32c":
		return "CRC32C"
	}
	return strings.ToUpper(a)
}

var algoStrength = map[string]int{"crc32c": 1, "md5": 2, "sha1": 3, "sha256": 4, "sha512": 5}

func newHash(algo string) hash.Hash {
	switch algo {
	case "md5":
		return md5.New()
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	case "sha512":
		return sha512.New()
	case "crc32c":
		return crc32.New(crc32.MakeTable(crc32.Castagnoli))
	}
	return nil
}

var hexRe = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// parseUserChecksum reads what the user typed: "abc123..." (the length
// tells the algorithm) or "sha256:abc..." / "SHA-256 abc..." / "md5=...".
// An empty text gives nil, nil.
func parseUserChecksum(s string) (*Checksum, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	algo := ""
	val := s
	if i := strings.IndexAny(s, ":= "); i > 0 {
		if a := normAlgo(s[:i]); a != "" {
			algo = a
			val = strings.TrimSpace(s[i+1:])
		}
	}
	// "sha256sum" output: "<hex>  file.iso"
	if f := strings.Fields(val); len(f) > 1 && hexRe.MatchString(f[0]) {
		val = f[0]
	}
	if !hexRe.MatchString(val) {
		return nil, errors.New("The checksum is not valid: use the hex text of an MD5, SHA-1, SHA-256 or SHA-512 hash.")
	}
	byLen := map[int]string{32: "md5", 40: "sha1", 64: "sha256", 128: "sha512"}[len(val)]
	if algo == "" {
		algo = byLen
	}
	if algo == "" || byLen != algo {
		return nil, errors.New("The checksum is not valid: an MD5 has 32 characters, SHA-1 40, SHA-256 64 and SHA-512 128.")
	}
	return &Checksum{Algo: algo, Hex: strings.ToLower(val), From: "user"}, nil
}

func normAlgo(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	a = strings.ReplaceAll(a, "-", "")
	switch a {
	case "md5", "sha1", "sha256", "sha512", "crc32c":
		return a
	case "sha":
		return "sha1"
	}
	return ""
}

func b64hex(v string) string {
	v = strings.Trim(strings.TrimSpace(v), ":\"")
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(v)
	}
	if err != nil || len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

// splitDigestList parses "sha-256=:abc=:, md5=xyz" and "SHA-256=abc,MD5=xyz".
func splitDigestList(h string) map[string]string {
	out := map[string]string{}
	for _, item := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			continue
		}
		if a := normAlgo(k); a != "" {
			if hx := b64hex(v); hx != "" {
				out[a] = hx
			}
		}
	}
	return out
}

func strongest(m map[string]string, from string) *Checksum {
	var best *Checksum
	for a, hx := range m {
		want := map[string]int{"md5": 32, "sha1": 40, "sha256": 64, "sha512": 128, "crc32c": 8}[a]
		if len(hx) != want {
			continue
		}
		if best == nil || algoStrength[a] > algoStrength[best.Algo] {
			best = &Checksum{Algo: a, Hex: hx, From: from}
		}
	}
	return best
}

// fileChecksum: a checksum of the whole file in a server answer. full is
// true when the answer carries the whole file (200, or HEAD), so that
// Content-MD5 describes the file and not only a piece.
func fileChecksum(h http.Header, full bool) *Checksum {
	var cands []*Checksum
	add := func(c *Checksum) {
		if c != nil {
			cands = append(cands, c)
		}
	}
	add(strongest(splitDigestList(h.Get("Repr-Digest")), "Repr-Digest"))
	add(strongest(splitDigestList(h.Get("Digest")), "Digest"))
	if g := h.Values("X-Goog-Hash"); len(g) > 0 {
		add(strongest(splitDigestList(strings.Join(g, ",")), "x-goog-hash"))
	}
	for _, x := range []struct{ hdr, algo string }{{"X-Checksum-Sha512", "sha512"}, {"X-Checksum-Sha256", "sha256"}, {"X-Checksum-Sha1", "sha1"}, {"X-Checksum-Md5", "md5"}} {
		if v := strings.TrimSpace(h.Get(x.hdr)); hexRe.MatchString(v) {
			add(strongest(map[string]string{x.algo: strings.ToLower(v)}, x.hdr))
		}
	}
	if full {
		add(strongest(splitDigestList(h.Get("Content-Digest")), "Content-Digest"))
		if v := b64hex(h.Get("Content-MD5")); v != "" {
			add(&Checksum{Algo: "md5", Hex: v, From: "Content-MD5"})
		}
	}
	var best *Checksum
	for _, c := range cands {
		if best == nil || algoStrength[c.Algo] > algoStrength[best.Algo] {
			best = c
		}
	}
	return best
}

// pieceChecksum: a checksum of just this answer's body (a range answer).
func pieceChecksum(h http.Header) *Checksum {
	if c := strongest(splitDigestList(h.Get("Content-Digest")), "Content-Digest"); c != nil {
		return c
	}
	if v := b64hex(h.Get("Content-MD5")); len(v) == 32 {
		return &Checksum{Algo: "md5", Hex: v, From: "Content-MD5"}
	}
	return nil
}

func sumHex(algo string, h hash.Hash) string {
	sum := h.Sum(nil)
	if algo == "crc32c" && len(sum) == 4 {
		return fmt.Sprintf("%08x", binary.BigEndian.Uint32(sum))
	}
	return hex.EncodeToString(sum)
}

// hashFile computes the checksum of a file, reporting progress.
func hashFile(ctx context.Context, path, algo string, progress func(done int64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := newHash(algo)
	buf := make([]byte, 1<<20)
	var done int64
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(done)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return sumHex(algo, h), nil
}

// hashMismatchError: the finished file does not match its checksum.
type hashMismatchError struct {
	want *Checksum
	got  string
}

func (e *hashMismatchError) Error() string {
	who := "the checksum you entered"
	if e.want.From != "user" {
		who = "the checksum the server sent (" + e.want.From + ")"
	}
	return fmt.Sprintf("Checksum mismatch: the downloaded file does not match %s (%s). BDDM downloaded the file again piece by piece and it still does not match, so the server's file is probably different from the one the checksum is for.",
		who, algoName(e.want.Algo))
}

// errPieceDamaged: a piece did not match the checksum the server sent for it.
var errPieceDamaged = errors.New("a downloaded piece was damaged (its checksum did not match)")

// repairBlock: the piece size used when looking for damaged pieces.
var repairBlock = int64(4 << 20)

// repair downloads the file again in pieces, compares each with what is on
// the disk and rewrites the pieces that differ. Returns how many differed.
func (d *download) repair(ctx context.Context) (int, error) {
	d.mu.Lock()
	size := d.State.Size
	d.mu.Unlock()
	if size <= 0 || !d.CanRange {
		return 0, errors.New("the server cannot send pieces of the file")
	}
	nBlocks := int((size + repairBlock - 1) / repairBlock)
	var next, damaged atomic.Int64
	var checked atomic.Int64
	workers := max(1, min(d.Workers, 4, nBlocks))
	var wg sync.WaitGroup
	var errOnce sync.Once
	var firstErr error
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				b := next.Add(1) - 1
				if b >= int64(nBlocks) || ctx.Err() != nil {
					return
				}
				from := b * repairBlock
				to := min(from+repairBlock, size) - 1
				var err error
				for try := 1; try <= maxTries; try++ {
					var bad bool
					bad, err = d.repairPiece(ctx, int(b)+w, from, to)
					if err == nil {
						if bad {
							damaged.Add(1)
						}
						break
					}
					if ctx.Err() != nil {
						return
					}
					if isNetErr(err) {
						if waited, werr := waitOnline(ctx, d.job, serverAddr(d.State.URL)); werr != nil {
							return
						} else if waited {
							try--
							continue
						}
					}
				}
				if err != nil {
					errOnce.Do(func() { firstErr = err; cancel() })
					return
				}
				c := checked.Add(1)
				d.job.setMessage("Looking for damaged pieces: %d of %d checked...", c, nBlocks)
			}
		}(w)
	}
	wg.Wait()
	if firstErr != nil {
		return int(damaged.Load()), firstErr
	}
	if err := ctx.Err(); err != nil {
		return int(damaged.Load()), err
	}
	return int(damaged.Load()), d.File.Sync()
}

// repairPiece downloads bytes from..to and rewrites them when the file
// differs. Reports whether the piece was damaged.
func (d *download) repairPiece(ctx context.Context, srcHint int, from, to int64) (bool, error) {
	src := d.sourceURL(srcHint)
	req, err := newRequest(ctx, http.MethodGet, src, d.hdr)
	if err != nil {
		return false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	resp, err := d.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	want := make([]byte, to-from+1)
	if _, err := io.ReadFull(resp.Body, want); err != nil {
		return false, err
	}
	have := make([]byte, len(want))
	if _, err := d.File.ReadAt(have, from); err != nil && err != io.EOF {
		return false, err
	}
	if bytes.Equal(want, have) {
		return false, nil
	}
	_, err = d.File.WriteAt(want, from)
	return true, err
}

// verify checks the finished .part file. When it does not match, damaged
// pieces are downloaded again (once). Returns a short line for the
// Downloads list ("SHA-256 checked") or a *hashMismatchError.
func (d *download) verify(ctx context.Context, path string, want *Checksum) (string, error) {
	name := algoName(want.Algo)
	who := "the server's checksum"
	if want.From == "user" {
		who = "your checksum"
	}
	check := func() (string, error) {
		if err := d.File.Sync(); err != nil {
			return "", err
		}
		size := d.State.Size
		return hashFile(ctx, path, want.Algo, func(done int64) {
			if size > 0 && done%(64<<20) < 1<<20 {
				d.job.setMessage("Checking the file (%s): %d%%...", name, done*100/size)
			}
		})
	}
	d.job.setMessage("Checking the file (%s)...", name)
	got, err := check()
	if err != nil {
		return "", err
	}
	if got == want.Hex {
		return name + " matches " + who, nil
	}
	d.job.setMessage("The file does not match %s; looking for damaged pieces...", who)
	n, err := d.repair(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("the file does not match %s, and the damaged pieces could not be downloaded again: %w", who, err)
	}
	if n > 0 {
		got, err = check()
		if err != nil {
			return "", err
		}
		if got == want.Hex {
			s := "s"
			if n == 1 {
				s = ""
			}
			return fmt.Sprintf("%s matches %s (%d damaged piece%s downloaded again)", name, who, n, s), nil
		}
	}
	return "", &hashMismatchError{want: want, got: got}
}

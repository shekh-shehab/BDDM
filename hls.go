package main

// hls.go: HLS (m3u8) playlist parsing and AES-128 segment decryption.
// BDDM downloads the segments itself (hlsdl.go) so the global speed limiter
// and auto-yield apply, then ffmpeg joins them into one MP4 (stream copy).

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errSampleAES: the stream uses an encryption method BDDM cannot decrypt
// (FairPlay / SAMPLE-AES / DRM). AES-128 CBC is supported.
var errSampleAES = errors.New("This stream uses SAMPLE-AES or DRM encryption, which BDDM cannot decrypt.")

// hlsKey is the encryption that applies to later segments.
type hlsKey struct {
	Method string // AES-128 or NONE
	URI    string
	IV     []byte // nil → use the segment sequence number
}

// hlsSeg is one media piece (or the EXT-X-MAP init segment).
type hlsSeg struct {
	URL  string
	Dur  float64
	Seq  int
	Off  int64
	Len  int64 // 0 = the whole file
	Key  *hlsKey
	Disc bool
}

// hlsMediaPL is a cleaned media playlist (links already absolute).
type hlsMediaPL struct {
	Target float64
	Seq0   int
	End    bool
	Map    *hlsSeg
	Segs   []hlsSeg
	Dur    float64
	FMP4   bool
}

func parseMediaPlaylist(text string) (*hlsMediaPL, error) {
	if !strings.Contains(text, "#EXTM3U") {
		return nil, errNotPlaylist
	}
	pl := &hlsMediaPL{Seq0: 0}
	seq := 0
	seqSet := false
	var curKey *hlsKey
	var dur float64
	var disc bool
	var brOff, brLen int64
	var haveBR bool

	for _, raw := range strings.Split(text, "\n") {
		l := strings.TrimSpace(raw)
		switch {
		case l == "":
		case strings.HasPrefix(l, "#EXT-X-TARGETDURATION:"):
			pl.Target, _ = strconv.ParseFloat(strings.TrimPrefix(l, "#EXT-X-TARGETDURATION:"), 64)
		case strings.HasPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, "#EXT-X-MEDIA-SEQUENCE:")))
			if err == nil {
				pl.Seq0, seq, seqSet = n, n, true
			}
		case strings.HasPrefix(l, "#EXT-X-ENDLIST"):
			pl.End = true
		case strings.HasPrefix(l, "#EXT-X-DISCONTINUITY"):
			disc = true
		case strings.HasPrefix(l, "#EXTINF:"):
			rest := strings.TrimPrefix(l, "#EXTINF:")
			if i := strings.IndexByte(rest, ','); i >= 0 {
				rest = rest[:i]
			}
			dur, _ = strconv.ParseFloat(rest, 64)
		case strings.HasPrefix(l, "#EXT-X-BYTERANGE:"):
			off, length, err := parseByteRange(strings.TrimPrefix(l, "#EXT-X-BYTERANGE:"), brOff)
			if err == nil {
				brOff, brLen, haveBR = off, length, true
			}
		case strings.HasPrefix(l, "#EXT-X-KEY:"):
			k, err := parseHLSKey(strings.TrimPrefix(l, "#EXT-X-KEY:"))
			if err != nil {
				return nil, err
			}
			curKey = k
		case strings.HasPrefix(l, "#EXT-X-MAP:"):
			a := parseHLSAttrs(strings.TrimPrefix(l, "#EXT-X-MAP:"))
			if a["URI"] == "" {
				continue
			}
			ms := &hlsSeg{URL: a["URI"], Key: curKey}
			if a["BYTERANGE"] != "" {
				off, length, err := parseByteRange(a["BYTERANGE"], 0)
				if err == nil {
					ms.Off, ms.Len = off, length
				}
			}
			pl.Map = ms
			pl.FMP4 = true
		case strings.HasPrefix(l, "#"):
			// comments and other tags
		default:
			if !seqSet {
				seqSet = true
			}
			s := hlsSeg{URL: l, Dur: dur, Seq: seq, Key: curKey, Disc: disc}
			if haveBR {
				s.Off, s.Len = brOff, brLen
				// the next implicit offset starts after this range
				brOff = brOff + brLen
			}
			pl.Segs = append(pl.Segs, s)
			pl.Dur += dur
			seq++
			dur, disc, haveBR = 0, false, false
		}
	}
	if len(pl.Segs) == 0 && pl.Map == nil {
		return nil, errors.New("This playlist has no video segments.")
	}
	return pl, nil
}

func parseByteRange(s string, prevEnd int64) (off, length int64, err error) {
	s = strings.TrimSpace(s)
	// "n@o" or "n" (offset is the byte after the previous range).
	n, o, hasOff := s, "", false
	if i := strings.IndexByte(s, '@'); i >= 0 {
		n, o, hasOff = s[:i], s[i+1:], true
	}
	length, err = strconv.ParseInt(strings.TrimSpace(n), 10, 64)
	if err != nil || length <= 0 {
		return 0, 0, fmt.Errorf("bad byte range %q", s)
	}
	if hasOff {
		off, err = strconv.ParseInt(strings.TrimSpace(o), 10, 64)
		if err != nil || off < 0 {
			return 0, 0, fmt.Errorf("bad byte range %q", s)
		}
		return off, length, nil
	}
	return prevEnd, length, nil
}

func parseHLSKey(s string) (*hlsKey, error) {
	a := parseHLSAttrs(s)
	method := strings.ToUpper(strings.TrimSpace(a["METHOD"]))
	switch method {
	case "", "NONE":
		return nil, nil
	case "AES-128":
	default:
		return nil, errSampleAES
	}
	if kf := a["KEYFORMAT"]; kf != "" && !strings.EqualFold(kf, "identity") {
		return nil, errSampleAES
	}
	k := &hlsKey{Method: "AES-128", URI: a["URI"]}
	if k.URI == "" {
		return nil, errors.New("This playlist's encryption key has no address.")
	}
	if iv := a["IV"]; iv != "" {
		b, err := parseHLSIV(iv)
		if err != nil {
			return nil, err
		}
		k.IV = b
	}
	return k, nil
}

func parseHLSIV(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("bad IV: %w", err)
	}
	if len(b) > 16 {
		b = b[len(b)-16:]
	}
	if len(b) < 16 {
		p := make([]byte, 16)
		copy(p[16-len(b):], b)
		b = p
	}
	return b, nil
}

func defaultSegIV(seq int) []byte {
	// HLS: the IV is the 128-bit media sequence number (big-endian).
	iv := make([]byte, 16)
	binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	return iv
}

func segmentIV(k *hlsKey, seq int) []byte {
	if k != nil && len(k.IV) == 16 {
		return k.IV
	}
	return defaultSegIV(seq)
}

func decryptAES128(data, key, iv []byte) ([]byte, error) {
	if len(key) != aes.BlockSize {
		return nil, fmt.Errorf("AES-128 key is %d bytes, want 16", len(key))
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("AES-128 IV is %d bytes, want 16", len(iv))
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("encrypted segment is not a full AES block")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return pkcs7Unpad(out)
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty AES payload")
	}
	pad := int(b[len(b)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(b) {
		return nil, errors.New("bad PKCS7 padding")
	}
	for i := 0; i < pad; i++ {
		if b[len(b)-1-i] != byte(pad) {
			return nil, errors.New("bad PKCS7 padding")
		}
	}
	return b[:len(b)-pad], nil
}

// parseHLSAttrs reads METHOD=AES-128,URI="...",IV=0xab...
func parseHLSAttrs(s string) map[string]string {
	out := map[string]string{}
	i, n := 0, len(s)
	for i < n {
		for i < n && (s[i] == ',' || s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(s[i : i+eq])
		i += eq + 1
		var val string
		if i < n && s[i] == '"' {
			i++
			j := i
			for j < n && s[j] != '"' {
				j++
			}
			val = s[i:j]
			i = j
			if i < n && s[i] == '"' {
				i++
			}
		} else {
			j := i
			for j < n && s[j] != ',' {
				j++
			}
			val = strings.TrimSpace(s[i:j])
			i = j
		}
		if key != "" {
			out[strings.ToUpper(key)] = val
		}
	}
	return out
}

// pickHLSQuality chooses a variant from a master list (already highest first).
// "best" / "" is the highest; a height like "720" is the highest at that
// height or below, or the lowest when every variant is higher.
func pickHLSQuality(qs []hlsQuality, want string) (hlsQuality, bool) {
	if len(qs) == 0 {
		return hlsQuality{}, false
	}
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" || want == "best" {
		return qs[0], true
	}
	h, err := strconv.Atoi(want)
	if err != nil || h <= 0 {
		return qs[0], true
	}
	for _, q := range qs {
		if q.Height > 0 && q.Height <= h {
			return q, true
		}
	}
	return qs[len(qs)-1], true
}

// audioURIFromMaster returns the default (or first) audio playlist URI of
// the given GROUP-ID, or "" when the video already has audio (no URI).
func audioURIFromMaster(text, group string) string {
	if group == "" {
		return ""
	}
	var best, any string
	for _, raw := range strings.Split(text, "\n") {
		l := strings.TrimSpace(raw)
		if !strings.HasPrefix(l, "#EXT-X-MEDIA:") {
			continue
		}
		a := parseHLSAttrs(strings.TrimPrefix(l, "#EXT-X-MEDIA:"))
		if !strings.EqualFold(a["TYPE"], "AUDIO") {
			continue
		}
		if a["GROUP-ID"] != group {
			continue
		}
		uri := a["URI"]
		if uri == "" {
			continue
		}
		any = uri
		if strings.EqualFold(a["DEFAULT"], "YES") || strings.EqualFold(a["AUTOSELECT"], "YES") {
			best = uri
		}
	}
	if best != "" {
		return best
	}
	return any
}

// stripImagePrefix drops a fake picture header in front of MPEG-TS data
// (movielink / megaplay-style disguised pieces). Other data is unchanged.
func stripImagePrefix(b []byte) []byte {
	if !imageMagic(b) {
		return b
	}
	for i := 4; i+2*188 < len(b) && i < 64<<10; i++ {
		if b[i] == 0x47 && b[i+188] == 0x47 && b[i+2*188] == 0x47 {
			return b[i:]
		}
	}
	return b
}

// useNativeHLS: a direct HLS stream (not DASH, not audio-only, not a
// whole site playlist) that we can join with ffmpeg.
func useNativeHLS(m *Manager, j *Job) bool {
	if m == nil || j == nil || !isHLSJob(j) {
		return false
	}
	if opt := j.Video; opt != nil {
		q := strings.ToLower(strings.TrimSpace(opt.Quality))
		if q == "mp3" || q == "m4a" || opt.Playlist {
			return false
		}
	}
	return m.tools.Find("ffmpeg") != ""
}

package main

import (
	"strings"
	"testing"
)

func TestCleanPlaylist(t *testing.T) {
	body := "\xef\xbb\xbfjunk-before-the-header\r\n#EXTM3U\r\n#EXT-X-VERSION:3\r\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x1\r\n" +
		"#EXT-X-MAP:URI=\"/init.mp4\"\r\n" +
		"#EXTINF:4.0,\r\nseg-1.ts?x=1\r\n" +
		"#EXTINF:4.0,\r\n../other/seg-2.ts\r\n" +
		"#EXTINF:4.0,\r\nhttps://cdn.example.org/seg-3.ts\r\n#EXT-X-ENDLIST\r\n"
	got, err := cleanPlaylist([]byte(body), "https://media.example.com/hls/v1/index.m3u8?token=abc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "#EXTM3U\n") {
		t.Fatalf("junk not removed: %q", got)
	}
	for _, want := range []string{
		`URI="https://media.example.com/hls/v1/key.bin"`,
		`URI="https://media.example.com/init.mp4"`,
		"\nhttps://media.example.com/hls/v1/seg-1.ts?x=1\n",
		"\nhttps://media.example.com/hls/other/seg-2.ts\n",
		"\nhttps://cdn.example.org/seg-3.ts\n",
		"#EXT-X-ENDLIST\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\r") {
		t.Errorf("CR left in output")
	}
}

func TestCleanPlaylistMaster(t *testing.T) {
	body := "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"audio/native.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=800000,AUDIO=\"a\"\nvideo.m3u8\n"
	got, err := cleanPlaylist([]byte(body), "https://x.test/_v8/abc/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `URI="https://x.test/_v8/abc/audio/native.m3u8"`) ||
		!strings.Contains(got, "\nhttps://x.test/_v8/abc/video.m3u8\n") {
		t.Errorf("bad master: %s", got)
	}
}

func TestNotPlaylistReasons(t *testing.T) {
	if _, err := cleanPlaylist([]byte("jsE/SB6pC5u5o8t7TRcHQJc+wJwbEKrEbzsTTAfvv2ngwSNVEqAK"), "https://x.test/a.m3u8"); err != errNotPlaylist {
		t.Fatalf("want errNotPlaylist, got %v", err)
	}
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{200, "jsE/SB6pC5u5o8t7TRcHQJc+wJwbEKrEbzsTTAfvv2ngwSNVEqAKyMqjrm41C2NZ", msgEncryptedStream},
		{403, `{"error":"Invalid token"}`, msgStreamRefused},
		{404, `{"error":"Invalid token"}`, msgExpiredStream},
		{200, `{"error":"token expired"}`, msgExpiredStream},
		{200, "<!DOCTYPE html><html><body>hi</body></html>", msgNotPlaylist},
	}
	for _, c := range cases {
		if got := explainNotPlaylist(c.status, []byte(c.body)).Error(); got != c.want {
			t.Errorf("%d %q: got %q", c.status, c.body, got)
		}
	}
}

func TestFriendlyVideoErrorTail(t *testing.T) {
	in := "Response data has no m3u header; please report this issue on  https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using  yt-dlp -U"
	if got := friendlyVideoError(in); got != msgNotPlaylist {
		t.Errorf("got %q", got)
	}
	got := friendlyVideoError("Something odd happened; please report this issue on https://github.com/yt-dlp/yt-dlp/issues?q= , filling out")
	if strings.Contains(got, "github") || !strings.HasPrefix(got, "Something odd happened.") {
		t.Errorf("got %q", got)
	}
}

func TestFileURL(t *testing.T) {
	if got := fileURL(`/tmp/a b/playlist.m3u8`); got != "file:///tmp/a%20b/playlist.m3u8" {
		t.Errorf("got %q", got)
	}
}

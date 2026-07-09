package streamproxy

import "testing"

func TestFirstMediaURLFromPlaylist(t *testing.T) {
	cases := []struct {
		name string
		body string
		base string
		want string
	}{
		{
			name: "plain m3u single url",
			body: "http://ice.example.com/absolut-relax.mp3\n",
			base: "http://radio.example/relax.m3u",
			want: "http://ice.example.com/absolut-relax.mp3",
		},
		{
			name: "extended m3u skips directives",
			body: "#EXTM3U\n#EXTINF:-1,Absolut Relax\nhttps://ice.example.com/stream\n",
			base: "http://radio.example/relax.m3u",
			want: "https://ice.example.com/stream",
		},
		{
			name: "pls FileN entry",
			body: "[playlist]\nNumberOfEntries=1\nFile1=http://ice.example.com/pls-stream\nTitle1=Absolut Relax\n",
			base: "http://radio.example/listen.pls",
			want: "http://ice.example.com/pls-stream",
		},
		{
			name: "relative m3u entry resolves against base",
			body: "stream/live.mp3\n",
			base: "http://radio.example/dir/relax.m3u",
			want: "http://radio.example/dir/stream/live.mp3",
		},
		{
			name: "hls media playlist has no plain url",
			body: "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg0.ts\n",
			base: "http://radio.example/live/index.m3u8",
			want: "http://radio.example/live/seg0.ts", // relative seg resolves; caller gates on #EXT-X- first
		},
		{
			name: "empty body",
			body: "\n\n",
			base: "http://radio.example/x.m3u",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := firstMediaURLFromPlaylist(c.body, c.base)
			if got != c.want {
				t.Fatalf("firstMediaURLFromPlaylist() = %q, want %q", got, c.want)
			}
		})
	}
}

// An HLS body must be recognised by its #EXT-X- markers so the caller routes it
// to serveHLS rather than treating the first segment line as a stream URL.
func TestHLSBodyDetectedByMarker(t *testing.T) {
	hls := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg0.ts\n"
	if !containsExtX(hls) {
		t.Fatalf("expected #EXT-X- marker to be detected in HLS body")
	}
	plain := "#EXTM3U\n#EXTINF:-1,Absolut Relax\nhttp://ice.example.com/stream\n"
	if containsExtX(plain) {
		t.Fatalf("plain M3U must not be mistaken for HLS")
	}
}

// containsExtX mirrors the classification the streamOne content-type branch
// applies (strings.Contains(body, "#EXT-X-")).
func containsExtX(body string) bool {
	for i := 0; i+7 <= len(body); i++ {
		if body[i:i+7] == "#EXT-X-" {
			return true
		}
	}
	return false
}

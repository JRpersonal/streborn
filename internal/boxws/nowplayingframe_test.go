package boxws

import (
	"context"
	"testing"
)

type nowPlayingRecorder struct {
	recHandler
	frames []NowPlayingFrame
}

func (r *nowPlayingRecorder) OnNowPlayingFrame(f NowPlayingFrame) { r.frames = append(r.frames, f) }

func TestNowPlayingElementCut(t *testing.T) {
	frame := []byte(`<updates deviceID="device-id-here"><nowPlayingUpdated deviceID="device-id-here"><nowPlaying deviceID="device-id-here" source="UPNP"><ContentItem source="UPNP" location="http://192.0.2.1:8888/spotify/stream.ogg"><itemName>Mix</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying></nowPlayingUpdated></updates>`)
	got := string(nowPlayingElement(frame))
	want := `<nowPlaying deviceID="device-id-here" source="UPNP"><ContentItem source="UPNP" location="http://192.0.2.1:8888/spotify/stream.ogg"><itemName>Mix</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`
	if got != want {
		t.Fatalf("cut =\n%s\nwant\n%s", got, want)
	}
	if nowPlayingElement([]byte(`<updates><volumeUpdated/></updates>`)) != nil {
		t.Fatal("a frame without nowPlaying must cut nothing")
	}
}

func TestNowPlayingFrameReachesHandler(t *testing.T) {
	rec := &nowPlayingRecorder{}
	c := newTestClient(rec)
	c.handleMessage(context.Background(), []byte(`<updates deviceID="x"><nowPlayingUpdated deviceID="x"><nowPlaying deviceID="x" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying></nowPlayingUpdated></updates>`))
	if len(rec.frames) != 1 || rec.frames[0].At.IsZero() {
		t.Fatalf("frames = %+v, want one", rec.frames)
	}
	c.handleMessage(context.Background(), []byte(`<updates deviceID="x"><volumeUpdated><volume><targetvolume>4</targetvolume></volume></volumeUpdated></updates>`))
	if len(rec.frames) != 1 {
		t.Fatalf("a volume frame must not reach the hook, frames = %d", len(rec.frames))
	}
}

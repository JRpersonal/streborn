package recallintent

import (
	"context"
	"testing"
	"time"
)

func TestKeepTrack(t *testing.T) {
	if KeepTrack(context.Background()) {
		t.Fatal("a plain context keeps the track")
	}
	ctx := WithKeepTrack(context.Background())
	if !KeepTrack(ctx) || !KeepTrack(context.WithoutCancel(ctx)) {
		t.Fatal("the mark did not survive")
	}
}

func TestPressSince(t *testing.T) {
	if _, ok := PressSince(context.Background()); ok {
		t.Fatal("a plain context carries no press")
	}
	if _, ok := PressSince(WithPressSince(context.Background(), time.Time{})); ok {
		t.Fatal("a zero time is no press")
	}
	at := time.Date(2026, 11, 22, 20, 22, 19, 0, time.UTC)
	got, ok := PressSince(context.WithoutCancel(WithPressSince(context.Background(), at)))
	if !ok || !got.Equal(at) {
		t.Fatalf("PressSince = %v, %v; want %v, true", got, ok, at)
	}
}

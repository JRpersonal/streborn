package recallintent

import (
	"context"
	"testing"
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

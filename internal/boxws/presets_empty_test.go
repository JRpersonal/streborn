package boxws

import (
	"context"
	"testing"
)

// An EMPTY presetsUpdated frame reaches the handler (#882). It used to be
// dropped in the dispatcher, so the agent's box-preset cache kept the last
// non-empty list for good after the firmware had dropped every key, and the
// desktop drew six playable stations on a speaker whose keys were gone.
func TestHandleMessage_EmptyPresetsUpdatedReachesHandler(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates><presetsUpdated><presets></presets></presetsUpdated></updates>`))
	if len(h.boxPresets) != 1 {
		t.Fatalf("expected one OnPresetsChanged call for the empty frame, got %d", len(h.boxPresets))
	}
	if len(h.boxPresets[0]) != 0 {
		t.Fatalf("the empty frame must arrive as an empty list, got %+v", h.boxPresets[0])
	}
	// The self-closing spelling the firmware also uses.
	c.handleMessage(context.Background(), []byte(`<updates><presetsUpdated><presets/></presetsUpdated></updates>`))
	if len(h.boxPresets) != 2 || len(h.boxPresets[1]) != 0 {
		t.Fatalf("the self-closing empty frame must arrive as an empty list too, got %+v", h.boxPresets)
	}
}

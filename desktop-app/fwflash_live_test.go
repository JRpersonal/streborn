package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveFirmwareFlash reflashes a real speaker with Bose's last firmware
// through the same path the app's Update now button takes. It rewrites the
// speaker's firmware, so it only runs when STR_LIVE_FWFLASH names the host.
func TestLiveFirmwareFlash(t *testing.T) {
	host := os.Getenv("STR_LIVE_FWFLASH")
	if host == "" {
		t.Skip("set STR_LIVE_FWFLASH to a speaker's address to reflash it")
	}
	a := NewApp()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	start := time.Now()
	res, err := a.flashBoseFirmware(ctx, host, true)
	if err != nil {
		t.Fatalf("flash after %s: %v", time.Since(start).Round(time.Second), err)
	}
	t.Logf("flashed %s via %s: %s -> %s in %s", host, res.Via, res.Before, res.After, time.Since(start).Round(time.Second))
}

package main

// Keeping the speaker's standby clock as its owner set it (#979) and its
// brightness (#1226); the logic lives in internal/standbyclock. This file
// only wires it: one keeper per agent, started once, and told about the
// standby and wake transitions the agent already hears.

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/clocksync"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/standbyclock"
)

// clockKeeper is the agent's keeper, nil until startStandbyClock ran.
var clockKeeper atomic.Pointer[standbyclock.Keeper]

// startStandbyClock creates the keeper, hands it to the web UI (via set) and
// starts the once-per-start re-apply in the background.
func startStandbyClock(ctx context.Context, boxHost string, logger *slog.Logger, set func(*standbyclock.Keeper)) *standbyclock.Keeper {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	k := standbyclock.New(boxapi.New(boxHost), logger, standbyclock.Options{
		OnBrightness: oled.SetTrackBrightness,
		Plausible:    func(t time.Time) bool { return !clocksync.Implausible(t) },
	})
	if set != nil {
		set(k)
	}
	clockKeeper.Store(k)
	go k.Start(ctx)
	return k
}

// standbyClockOnPower is a standby or wake transition: the keeper reads the
// clock once (rate-limited) and puts the stored choice back only if the
// firmware dropped it. Never blocks.
func standbyClockOnPower(reason string) {
	if k := clockKeeper.Load(); k != nil {
		k.Event(context.Background(), reason)
	}
}

#!/bin/sh
# Prepares a JRpersonal/go-librespot clone for the STR-LEAN build (pure Go,
# CGO_ENABLED=0, GOARM=5 softfloat). Shared by go-librespot.yml and
# release.yml so the two engine builds cannot drift apart.
#
# Expects to run from the STR repo root with the fork cloned into
# ./go-librespot. Drops the cgo code paths STR never executes and stubs the
# symbols the rest of the build references, so the packages still compile:
#   - ALSA output driver: STR drives playback through the pipe_passthrough
#     backend only (no functional loss, and Alpine never had a static
#     libasound anyway);
#   - Vorbis decoder (xlab/vorbis-go -> libvorbis/libogg): passthrough hands
#     the raw Ogg to the pipe, the decoder is never constructed;
#   - FLAC decoder (inline cgo -> libFLAC): unreachable, the cgo-free patch
#     pins the format selection to Vorbis under passthrough;
#   - MP3 decoder (inline cgo -> libmpg123, upstream 2026-08): same story as
#     FLAC, only Vorbis is ever selected under passthrough.
# The stubs return a clear error should any future code path still try to
# construct a decoder. Finally applies the cgo-free patch (pure-Go Ogg
# metadata-page parser + the passthrough-pins-Vorbis guard, with tests).
set -eu

rm -f go-librespot/output/driver-alsa.go go-librespot/output/mixer-alsa.go \
      go-librespot/vorbis/decoder.go go-librespot/flac/decoder.go \
      go-librespot/mp3/decoder.go

cat > go-librespot/output/driver-alsa-noalsa.go <<'GO'
//go:build !android && !darwin && !js && !windows && !nintendosdk

package output

import "fmt"

func newAlsaOutput(*NewOutputOptions) (Output, error) {
	return nil, fmt.Errorf("alsa backend not compiled in this build")
}
GO

cat > go-librespot/vorbis/decoder-stub.go <<'GO'
package vorbis

import (
	"errors"

	librespot "github.com/devgianlu/go-librespot"
)

const DataChunkSize = 4096

var errNoDecoder = errors.New("vorbis decoder not compiled in this build (pipe passthrough only)")

type Decoder struct {
	SampleRate int32
	Channels   int32
}

func New(librespot.Logger, librespot.SizedReadAtSeeker, *MetadataPage, float32) (*Decoder, error) {
	return nil, errNoDecoder
}

func (d *Decoder) Read([]float32) (int, error) { return 0, errNoDecoder }

func (d *Decoder) SetPositionMs(int64) error { return errNoDecoder }

func (d *Decoder) PositionMs() int64 { return 0 }

func (d *Decoder) Close() {}
GO

cat > go-librespot/flac/decoder-stub.go <<'GO'
package flac

import (
	"errors"

	librespot "github.com/devgianlu/go-librespot"
)

var errNoDecoder = errors.New("flac decoder not compiled in this build (pipe passthrough only)")

type Decoder struct {
	SampleRate int32
	Channels   int32
	BitDepth   int32
}

func New(librespot.Logger, librespot.SizedReadAtSeeker, float32) (*Decoder, error) {
	return nil, errNoDecoder
}

func (d *Decoder) Read([]float32) (int, error) { return 0, errNoDecoder }

func (d *Decoder) SetPositionMs(int64) error { return errNoDecoder }

func (d *Decoder) PositionMs() int64 { return 0 }

func (d *Decoder) Close() error { return nil }
GO

cat > go-librespot/mp3/decoder-stub.go <<'GO'
package mp3

import (
	"errors"

	librespot "github.com/devgianlu/go-librespot"
)

var errNoDecoder = errors.New("mp3 decoder not compiled in this build (pipe passthrough only)")

type Decoder struct {
	SampleRate int32
	Channels   int32
}

func New(librespot.Logger, librespot.SizedReadAtSeeker, float32) (*Decoder, error) {
	return nil, errNoDecoder
}

func (d *Decoder) Read([]float32) (int, error) { return 0, errNoDecoder }

func (d *Decoder) SetPositionMs(int64) error { return errNoDecoder }

func (d *Decoder) PositionMs() int64 { return 0 }

func (d *Decoder) Close() error { return nil }
GO

git -C go-librespot apply --stat ../.github/patches/go-librespot-cgo-free.patch
git -C go-librespot apply ../.github/patches/go-librespot-cgo-free.patch

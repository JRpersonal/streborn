// Command machouuid gives a macOS executable the same Mach-O UUID in every
// release.
//
// macOS decides whether an app may talk to devices on the local network (the
// Local Network permission) from the app's code signature and, in Apple's own
// words, "uses your main executable UUID as part of its implementation"
// (TN3179). The linker derives that UUID from the binary's content, so every
// release of the desktop app carried a new one. Mac users kept reporting the
// same thing after an in-app update: the app is still switched on under
// Privacy & Security > Local Network, and macOS refuses every connection to a
// speaker with "no route to host" until the switch is turned off and on.
//
// The UUID has no other job in a stripped release build (it pairs a binary
// with its debug symbols), so the release pipeline pins it: one fixed value
// per CPU architecture, derived from the bundle identifier. It runs on the
// unsigned binary, before codesign; changing a signed binary would break its
// signature.
//
// Usage:
//
//	machouuid -id de.st-reborn.app "ST Reborn.app/Contents/MacOS/ST Reborn"
//
// It exits non-zero when any slice has no LC_UUID to pin, so a toolchain
// change that drops the load command fails the build instead of shipping an
// app without one.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
)

const (
	fatMagic   = 0xcafebabe // big-endian on disk
	fatMagic64 = 0xcafebabf
	machMagic  = 0xfeedfacf // 64-bit, little-endian on disk
	lcUUID     = 0x1b

	machHeaderSize = 32
)

// pinned is one slice whose UUID was written.
type pinned struct {
	CPU  uint32
	Was  [16]byte
	Now  [16]byte
	Arch string
}

// stableUUID is the UUID a slice of the app with this bundle identifier gets.
// Each architecture has its own, as the linker gives a universal binary; two
// programs must not share one.
func stableUUID(bundleID string, cpu uint32) [16]byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("machouuid/%s/cpu-%#x", bundleID, cpu)))
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x80 // version 8: a custom, name-derived UUID
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return u
}

func archName(cpu uint32) string {
	switch cpu {
	case 0x01000007:
		return "x86_64"
	case 0x0100000c:
		return "arm64"
	}
	return fmt.Sprintf("cpu-%#x", cpu)
}

// pin rewrites the LC_UUID of every slice in a thin or universal Mach-O image
// in place and reports what it changed.
func pin(img []byte, bundleID string) ([]pinned, error) {
	if len(img) < 8 {
		return nil, errors.New("file is too short to be a Mach-O image")
	}
	switch binary.BigEndian.Uint32(img[0:4]) {
	case fatMagic, fatMagic64:
		wide := binary.BigEndian.Uint32(img[0:4]) == fatMagic64
		n := int(binary.BigEndian.Uint32(img[4:8]))
		entry := 20
		if wide {
			entry = 32
		}
		if n == 0 || n > 16 || len(img) < 8+n*entry {
			return nil, fmt.Errorf("universal header names %d slices, which this file cannot hold", n)
		}
		var out []pinned
		for i := 0; i < n; i++ {
			e := img[8+i*entry:]
			var off, size uint64
			if wide {
				off, size = binary.BigEndian.Uint64(e[8:16]), binary.BigEndian.Uint64(e[16:24])
			} else {
				off, size = uint64(binary.BigEndian.Uint32(e[8:12])), uint64(binary.BigEndian.Uint32(e[12:16]))
			}
			if off > uint64(len(img)) || size > uint64(len(img))-off {
				return nil, fmt.Errorf("slice %d lies outside the file", i)
			}
			p, err := pinSlice(img[off:off+size], bundleID)
			if err != nil {
				return nil, fmt.Errorf("slice %d: %w", i, err)
			}
			out = append(out, p)
		}
		return out, nil
	}
	p, err := pinSlice(img, bundleID)
	if err != nil {
		return nil, err
	}
	return []pinned{p}, nil
}

// pinSlice rewrites the LC_UUID of one 64-bit Mach-O image.
func pinSlice(s []byte, bundleID string) (pinned, error) {
	if len(s) < machHeaderSize || binary.LittleEndian.Uint32(s[0:4]) != machMagic {
		return pinned{}, errors.New("not a 64-bit little-endian Mach-O image")
	}
	cpu := binary.LittleEndian.Uint32(s[4:8])
	ncmds := binary.LittleEndian.Uint32(s[16:20])
	sizeofcmds := uint64(binary.LittleEndian.Uint32(s[20:24]))
	if uint64(len(s)) < machHeaderSize+sizeofcmds {
		return pinned{}, errors.New("load commands run past the end of the image")
	}
	cmds := s[machHeaderSize : machHeaderSize+sizeofcmds]
	off := uint64(0)
	for i := uint32(0); i < ncmds; i++ {
		if off+8 > uint64(len(cmds)) {
			return pinned{}, errors.New("load command table is cut short")
		}
		cmd := binary.LittleEndian.Uint32(cmds[off : off+4])
		size := uint64(binary.LittleEndian.Uint32(cmds[off+4 : off+8]))
		if size < 8 || off+size > uint64(len(cmds)) {
			return pinned{}, fmt.Errorf("load command %d has an impossible size %d", i, size)
		}
		if cmd == lcUUID {
			if size != 24 {
				return pinned{}, fmt.Errorf("LC_UUID has size %d, expected 24", size)
			}
			p := pinned{CPU: cpu, Arch: archName(cpu), Now: stableUUID(bundleID, cpu)}
			copy(p.Was[:], cmds[off+8:off+24])
			copy(cmds[off+8:off+24], p.Now[:])
			return p, nil
		}
		off += size
	}
	return pinned{}, fmt.Errorf("the %s image has no LC_UUID load command", archName(cpu))
}

func run(args []string) error {
	fs := flag.NewFlagSet("machouuid", flag.ContinueOnError)
	id := fs.String("id", "", "bundle identifier the UUID is derived from (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || fs.NArg() != 1 {
		return errors.New("usage: machouuid -id <bundle identifier> <executable>")
	}
	path := fs.Arg(0)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	img, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	done, err := pin(img, *id)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.WriteFile(path, img, info.Mode().Perm()); err != nil {
		return err
	}
	for _, p := range done {
		fmt.Printf("%s %-7s LC_UUID %x -> %x\n", path, p.Arch, p.Was, p.Now)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "machouuid:", err)
		os.Exit(1)
	}
}

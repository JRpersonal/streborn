package main

// The Mach-O UUID of the running executable, for the start line of the log.
//
// macOS keeps the Local Network permission per app and uses the main
// executable's UUID for it. The release pipeline pins that UUID (cmd/machouuid)
// so an update is the same program to macOS as the version before it. When a
// Mac user reports that the app lost its speakers after an update, this line
// says which UUID the app that failed was carrying: the pinned one, or a
// build's own.

import (
	"debug/macho"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
)

// executableUUID returns the LC_UUID of the slice of this executable that
// matches the running architecture, as 32 hex digits. Empty on every platform
// whose executables are not Mach-O, and when it cannot be read.
func executableUUID() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return machoUUIDOf(exe, runtime.GOARCH)
}

// machoUUIDOf reads the UUID from a thin or universal Mach-O file.
func machoUUIDOf(path, goarch string) string {
	want := macho.CpuArm64
	if goarch == "amd64" {
		want = macho.CpuAmd64
	}
	if fat, err := macho.OpenFat(path); err == nil {
		defer fat.Close()
		for _, a := range fat.Arches {
			if a.Cpu == want {
				return uuidOfMachO(a.File)
			}
		}
		return ""
	}
	f, err := macho.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return uuidOfMachO(f)
}

func uuidOfMachO(f *macho.File) string {
	const lcUUID = 0x1b
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) == 24 && binary.LittleEndian.Uint32(raw[0:4]) == lcUUID {
			return fmt.Sprintf("%x", raw[8:24])
		}
	}
	return ""
}

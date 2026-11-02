package presets

import "testing"

func folderPreset(udn, container string, urls ...string) Preset {
	p := Preset{Name: "Power Windows", Type: TypeQueue}
	if udn != "" || container != "" {
		p.Folder = &FolderRef{ServerUDN: udn, ContainerID: container}
	}
	for _, u := range urls {
		p.Items = append(p.Items, PresetItem{URL: u})
	}
	return p
}

// The one-folder-one-key rule (#1695): which two folder keys count as the same
// folder, and which stay allowed side by side.
func TestSameFolder(t *testing.T) {
	const (
		minim  = "uuid:00000000-0000-0000-0000-00000000000a"
		other  = "uuid:00000000-0000-0000-0000-00000000000b"
		trackA = "http://192.0.2.10:9790/minimserver/*/music/a.flac"
		trackB = "http://192.0.2.10:9790/minimserver/*/music/b.flac"
		farA   = "http://192.0.2.20:8200/MediaItems/a.flac"
		farB   = "http://192.0.2.20:8200/MediaItems/b.flac"
	)
	shuffled := folderPreset(minim, "0$1$7", trackA, trackB)
	shuffled.Shuffle = true
	shuffled.Name = "renamed"
	tests := []struct {
		name string
		a, b Preset
		want bool
	}{
		{"same server and container", folderPreset(minim, "0$1$7", trackA, trackB), folderPreset(minim, "0$1$7", trackA), true},
		{"udn compared without prefix and case", folderPreset(minim, "0$1$7", trackA), folderPreset("UUID:00000000-0000-0000-0000-00000000000A", "0$1$7", trackB), true},
		{"shuffle and name do not make a second folder", folderPreset(minim, "0$1$7", trackA, trackB), shuffled, true},
		{"same album from a different server stays allowed", folderPreset(minim, "0$1$7", trackA, trackB), folderPreset(other, "0$1$7", farA, farB), false},
		{"different server even with the same track urls", folderPreset(minim, "0$1$7", trackA, trackB), folderPreset(other, "0$1$7", trackA, trackB), false},
		{"same server, other tree, same tracks", folderPreset(minim, "0$1$7", trackA, trackB), folderPreset(minim, "0$4$2", trackB, trackA), true},
		{"same server, other folder", folderPreset(minim, "0$1$7", trackA, trackB), folderPreset(minim, "0$1$8", trackA), false},
		{"no folder identity: identical track list", folderPreset("", "", trackA, trackB), folderPreset("", "", trackA, trackB), true},
		{"no folder identity: same tracks in another order", folderPreset("", "", trackA, trackB), folderPreset("", "", trackB, trackA), true},
		{"one side without identity falls back to the tracks", folderPreset(minim, "0$1$7", trackA, trackB), folderPreset("", "", trackA, trackB), true},
		{"no folder identity: one track more", folderPreset("", "", trackA, trackB), folderPreset("", "", trackA), false},
		{"no folder identity: other server's copy", folderPreset("", "", trackA, trackB), folderPreset("", "", farA, farB), false},
		{"no tracks at all is no match", folderPreset("", ""), folderPreset("", ""), false},
		{"a station is never a folder", Preset{Type: "radio", StreamURL: trackA}, folderPreset("", "", trackA), false},
	}
	for _, tc := range tests {
		if got := SameFolder(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: SameFolder = %v, want %v", tc.name, got, tc.want)
		}
		if got := SameFolder(tc.b, tc.a); got != tc.want {
			t.Errorf("%s (swapped): SameFolder = %v, want %v", tc.name, got, tc.want)
		}
	}
}

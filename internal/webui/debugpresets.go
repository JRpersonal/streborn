package webui

import (
	"fmt"

	"github.com/JRpersonal/streborn/anonymise"
	"github.com/JRpersonal/streborn/internal/presets"
)

// presetDebugLine is one slot of the "presets" debug section. account is the
// account a Spotify key was saved under, hashed here with the same function
// the log lines use, so the section and the log name one account with one
// token and the raw form of the section carries no account name (#1947). A
// key without an account prints account="".
func presetDebugLine(p presets.Preset) string {
	return fmt.Sprintf("slot %d: type=%s name=%q codec=%q stream=%q uri=%q account=%q items=%d",
		p.Slot, p.Type, p.Name, p.Codec, p.StreamURL, p.URI, anonymise.MaskAccount(p.Account), len(p.Items))
}

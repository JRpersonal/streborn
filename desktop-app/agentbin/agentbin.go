// Package agentbin embedded das compiled ARM Stick Agent Binary. Das
// streborn-armv7l File muss VOR dem Wails Build hier liegen
// (siehe Makefile Target wails-build oder scripts/build-desktop-app).
//
// Wenn das Binary nicht eingebettet ist (z.B. bei einem Dev Build),
// liefert Bytes() nil und WriteStickFiles muss auf einen externen Pfad
// zurueckgreifen.
package agentbin

import (
	_ "embed"
)

//go:embed streborn-armv7l
var armBinary []byte

// Bytes liefert das eingebettete ARM Binary. Empty wenn beim Build nicht
// vorhanden (lokal Dev ohne ARM Cross Compile).
func Bytes() []byte { return armBinary }

// Available true wenn das Binary verfuegbar ist.
func Available() bool { return len(armBinary) > 0 }

// Name ist der Dateiname unter dem das Binary auf dem Stick liegen muss.
const Name = "streborn-armv7l"

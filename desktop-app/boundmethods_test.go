package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// No bound App method may return two values unless the second is an error.
//
// Wails cannot express one. internal/binding/boundMethod.go, for an output
// count of two, keeps the FIRST result and treats the second as an error:
//
//	case 2:
//	    returnValue = callResults[0].Interface()
//	    if temp, ok := callResults[1].Interface().(error); ok { err = temp }
//
// So a second non-error value is dropped before it ever leaves Go. Nothing
// says so. The build is clean, the generated binding compiles, and the
// TypeScript it writes is `Promise<number|number>`, which looks like a type
// rather than like a value that is about to go missing.
//
// It went missing for months. TrackPosition returned (position, duration), the
// frontend destructured the answer as a pair, destructuring a plain number
// throws, and the throw landed in a catch that exists to keep the last reading
// on a missed poll. So the desktop progress bar never had a length and never
// drew, and the elapsed clock beside it was not a reading at all but pure
// client-side extrapolation. Reported three times and diagnosed wrong twice,
// because every other link in the chain was genuinely correct: the duration is
// sent when the track starts, the speaker answers with it, and the phone page,
// which fetches the endpoint itself and reads the fields by name, has always
// drawn the bar.
//
// One struct is the fix. This test is the fix for the next one.
func TestNoBoundMethodReturnsTwoValues(t *testing.T) {
	errType := reflect.TypeOf((*error)(nil)).Elem()
	appType := reflect.TypeOf(&App{})

	var bad []string
	for i := 0; i < appType.NumMethod(); i++ {
		m := appType.Method(i)
		ft := m.Type
		if ft.NumOut() < 2 {
			continue
		}
		if ft.NumOut() > 2 {
			bad = append(bad, m.Name+" returns "+strconv.Itoa(ft.NumOut())+" values; Wails keeps only the first")
			continue
		}
		if !ft.Out(1).Implements(errType) {
			bad = append(bad, m.Name+" returns ("+ft.Out(0).String()+", "+ft.Out(1).String()+
				"); the second is not an error, so Wails drops it silently. Return one struct.")
		}
	}
	if len(bad) > 0 {
		t.Errorf("bound methods whose second result never reaches the frontend:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

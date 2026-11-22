package usbmedia

import (
	"strings"
	"unicode/utf8"
)

// File names, as the kernel hands them over, are in the mount's iocharset, not
// necessarily UTF-8. The speaker firmware mounts a stick with
// iocharset=iso8859-1 (live /proc/mounts on ST10 and Portable,
// "codepage=437,iocharset=iso8859-1,shortname=mixed"), so "01 Ärger.mp3"
// arrives as the Latin-1 byte 0xC4 and, read as UTF-8, showed as "01 �rger".
//
// So the package keeps two forms of every path apart:
//
//   - the object id and the title are UTF-8 text, decoded from the iocharset;
//   - the on-disk path (Entry.Path) is the bytes exactly as the filesystem
//     names them. Every file access uses it, and so do the file and cover
//     URLs, so the renderer fetches the same bytes it always did.
//
// A stick mounted utf8 has both forms equal and works as before.

// Charset names as NameCharset reports them.
const (
	CharsetUTF8    = "utf8"
	CharsetLatin1  = "iso8859-1"
	CharsetLatin9  = "iso8859-15"
	CharsetUnknown = ""
)

// NameCharset reads the file name charset from a vfat mount's options: the
// utf8 option wins (the kernel then hands out UTF-8 whatever iocharset says),
// otherwise iocharset. CharsetUnknown when neither is given or the charset is
// one this package does not convert; the names are then read as UTF-8 where
// they are valid UTF-8 and as Latin-1 where they are not.
func NameCharset(mountOptions string) string {
	charset, utf8Flag := "", false
	for _, o := range strings.Split(mountOptions, ",") {
		o = strings.ToLower(strings.TrimSpace(o))
		k, v, hasV := strings.Cut(o, "=")
		switch {
		case k == "utf8" && !hasV:
			utf8Flag = true
		case k == "utf8":
			switch v {
			case "1", "yes", "true":
				utf8Flag = true
			case "0", "no", "false":
				utf8Flag = false
			}
		case k == "iocharset":
			charset = v
		}
	}
	if utf8Flag {
		return CharsetUTF8
	}
	switch charset {
	case "utf8", "utf-8":
		return CharsetUTF8
	case "iso8859-1", "iso-8859-1", "latin1":
		return CharsetLatin1
	case "iso8859-15", "iso-8859-15", "latin9":
		return CharsetLatin9
	}
	return CharsetUnknown
}

// nameCodec converts between on-disk names and UTF-8 text.
type nameCodec struct {
	charset string
}

// decode turns an on-disk name into UTF-8 text.
func (c nameCodec) decode(raw string) string {
	switch c.charset {
	case CharsetUTF8:
		return raw
	case CharsetLatin1:
		return decodeSingleByte(raw, nil)
	case CharsetLatin9:
		return decodeSingleByte(raw, latin9Decode)
	}
	if utf8.ValidString(raw) {
		return raw
	}
	return decodeSingleByte(raw, nil)
}

// encode returns the on-disk names a UTF-8 text can stand for, the likeliest
// first. One for a known charset (none when the text cannot be written in it),
// up to two for an unknown one: the text itself, then its Latin-1 bytes.
func (c nameCodec) encode(text string) []string {
	switch c.charset {
	case CharsetUTF8:
		return []string{text}
	case CharsetLatin1:
		if raw, ok := encodeSingleByte(text, nil, nil); ok {
			return []string{raw}
		}
		return nil
	case CharsetLatin9:
		if raw, ok := encodeSingleByte(text, latin9Encode, latin9Decode); ok {
			return []string{raw}
		}
		return nil
	}
	out := []string{text}
	if raw, ok := encodeSingleByte(text, nil, nil); ok && raw != text {
		out = append(out, raw)
	}
	return out
}

// decodeSingleByte maps every byte to one rune: its own code point (Latin-1),
// or the one table names for it (the few places ISO 8859-15 differs).
func decodeSingleByte(raw string, table map[byte]rune) string {
	ascii := true
	for i := 0; i < len(raw); i++ {
		if raw[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return raw
	}
	b := make([]byte, 0, len(raw)+8)
	for i := 0; i < len(raw); i++ {
		r := rune(raw[i])
		if t, ok := table[raw[i]]; ok {
			r = t
		}
		b = utf8.AppendRune(b, r)
	}
	return string(b)
}

// encodeSingleByte is decodeSingleByte backwards: table is the reverse of
// decoded. ok is false for invalid UTF-8 and for a rune the charset cannot hold.
func encodeSingleByte(text string, table map[rune]byte, decoded map[byte]rune) (string, bool) {
	if !utf8.ValidString(text) {
		return "", false
	}
	b := make([]byte, 0, len(text))
	for _, r := range text {
		if v, ok := table[r]; ok {
			b = append(b, v)
			continue
		}
		if r > 0xFF {
			return "", false
		}
		if _, taken := decoded[byte(r)]; taken {
			// A byte the table re-assigns no longer stands for its own code point.
			return "", false
		}
		b = append(b, byte(r))
	}
	return string(b), true
}

// latin9Decode are the eight bytes where ISO 8859-15 differs from Latin-1.
var latin9Decode = map[byte]rune{
	0xA4: '€', 0xA6: 'Š', 0xA8: 'š', 0xB4: 'Ž', 0xB8: 'ž', 0xBC: 'Œ', 0xBD: 'œ', 0xBE: 'Ÿ',
}

var latin9Encode = func() map[rune]byte {
	m := make(map[rune]byte, len(latin9Decode))
	for b, r := range latin9Decode {
		m[r] = b
	}
	return m
}()

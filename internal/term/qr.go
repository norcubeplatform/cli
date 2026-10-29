package term

import (
	"strings"

	"rsc.io/qr"
)

// QR renders text as a QR code for a terminal: two modules per character
// row with half-block glyphs, black on a forced white background (ANSI
// colors) inside the quiet zone scanners need, so it scans the same on dark
// and light terminal themes. It returns "" when the code can't be encoded
// or wouldn't fit in maxWidth columns. Callers only use it when colors are
// on: without them the code could come out inverted.
func QR(text string, maxWidth int) string {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return ""
	}
	const quiet = 2
	size := code.Size + 2*quiet
	if size > maxWidth {
		return ""
	}
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
			return false
		}
		return code.Black(x, y)
	}
	const blackOnWhite, reset = "\x1b[30;107m", "\x1b[0m"
	var b strings.Builder
	for y := 0; y < size; y += 2 {
		b.WriteString(blackOnWhite)
		for x := 0; x < size; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString(reset)
		b.WriteByte('\n')
	}
	return b.String()
}

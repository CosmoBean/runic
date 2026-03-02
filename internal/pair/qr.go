package pair

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// GenerateToken creates a cryptographically random token.
func GenerateToken() (string, error) {
	b := make([]byte, 32) // 256 bits
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), nil
}

// GenerateQR prints a QR code to stdout using Unicode characters.
// The qrData is encoded into the QR code.
func GenerateQR(qrData string) error {
	qr, err := qrcode.New(qrData, qrcode.Medium)
	if err != nil {
		return err
	}

	bitmap := qr.Bitmap()
	rows := len(bitmap)

	// Use Unicode half-block characters to render 2 rows per line
	for y := 0; y < rows; y += 2 {
		for x := 0; x < len(bitmap[y]); x++ {
			top := bitmap[y][x]
			bottom := false
			if y+1 < rows {
				bottom = bitmap[y+1][x]
			}

			switch {
			case top && bottom:
				fmt.Print("\u2588") // full block (both dark)
			case top && !bottom:
				fmt.Print("\u2580") // upper half block
			case !top && bottom:
				fmt.Print("\u2584") // lower half block
			default:
				fmt.Print(" ") // both light
			}
		}
		fmt.Println()
	}
	return nil
}

// PairURL constructs the URL encoded in the QR code.
func PairURL(host string, port int, token string, machineName string) string {
	return fmt.Sprintf("runic://pair?url=wss://%s:%d&token=%s&name=%s",
		host, port, token, machineName)
}

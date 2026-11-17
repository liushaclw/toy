package main

import (
	"bytes"
	_ "embed"
	"image"
	"image/draw"
	"image/png"

	"github.com/vivaui/viva/app"
)

// The logo is drawn in assets/logo.svg; assets/icon.png (1024 px) is what
// viva build puts in the program, this 512 px copy what the window shows.
//
//go:embed assets/window-icon.png
var windowIconPNG []byte

// windowIcon decodes the embedded logo; a bad picture costs the window its icon.
func windowIcon() app.WindowIcon {
	decoded, err := png.Decode(bytes.NewReader(windowIconPNG))
	if err != nil || decoded.Bounds().Dx() != decoded.Bounds().Dy() {
		return app.WindowIcon{}
	}
	size := decoded.Bounds().Dx()
	rgba := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.Draw(rgba, rgba.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	return app.WindowIcon{Size: size, RGBA: rgba.Pix}
}

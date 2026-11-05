//go:build linux

// SPDX-License-Identifier: MIT

package provider

import (
	"image"
	"image/color"
	"math"
	"sync"
)

const cursorTileSize = 96
const cursorTileAnchor = 40

// Arrow geometry adapted from Cua Driver's default theme, under the MIT license.
// Copyright (c) 2025 Cua AI, Inc.
// See licenses/MIT-cua-cursor.txt.
// https://github.com/trycua/cua/blob/main/libs/cua-driver/rust/crates/cursor-overlay/assets/build_default_theme.py
var cursorOutline = [][3]cursorPoint{
	{{55, 30}, {0, 0}, {-7, -2}},
	{{43, 41}, {-1, -8}, {0, 0}},
	{{64, 98}, {0, 0}, {3, 8}},
	{{77, 99}, {-4, 7}, {0, 0}},
	{{86, 79}, {0, 0}, {2, -4}},
	{{95, 70}, {-4, 2}, {0, 0}},
	{{108, 63}, {0, 0}, {7, -4}},
	{{107, 50}, {7, 3}, {0, 0}},
}

var cursorArtwork struct {
	once   sync.Once
	images [3]*image.RGBA
}

func cursorAccent(failed bool) color.NRGBA {
	if failed {
		return color.NRGBA{R: 232, G: 82, B: 98, A: 255}
	}
	return color.NRGBA{R: 94, G: 192, B: 232, A: 255}
}

func cursorGlyph(pressed, failed bool) *image.RGBA {
	cursorArtwork.once.Do(func() {
		cursorArtwork.images[0] = makeCursorGlyph(1, false)
		cursorArtwork.images[1] = makeCursorGlyph(0.94, false)
		cursorArtwork.images[2] = makeCursorGlyph(1, true)
	})
	if failed {
		return cursorArtwork.images[2]
	}
	if pressed {
		return cursorArtwork.images[1]
	}
	return cursorArtwork.images[0]
}

func clampCursor(value float64) float64 { return math.Max(0, math.Min(1, value)) }

// blendCursor writes premultiplied alpha, which both Go RGBA and X11's ARGB
// compositor expect. Transparent edge pixels must not contain unscaled RGB.
func blendCursor(target *image.RGBA, x, y int, ink color.NRGBA, opacity float64) {
	if !image.Pt(x, y).In(target.Rect) {
		return
	}
	a := uint32(math.Round(clampCursor(opacity) * float64(ink.A)))
	if a == 0 {
		return
	}
	i := target.PixOffset(x, y)
	for channel, c := range [3]uint8{ink.R, ink.G, ink.B} {
		target.Pix[i+channel] = uint8((uint32(c)*a + uint32(target.Pix[i+channel])*(255-a) + 127) / 255)
	}
	target.Pix[i+3] = uint8(a + (uint32(target.Pix[i+3])*(255-a)+127)/255)
}

func cursorCurve(scale float64) []cursorPoint {
	var points []cursorPoint
	angle := 20 * math.Pi / 180
	transform := func(p cursorPoint) cursorPoint {
		x, y := p.x-55, p.y-30
		factor := 42.0 / 128 * scale
		return cursorPoint{(x*math.Cos(angle) - y*math.Sin(angle)) * factor, (x*math.Sin(angle) + y*math.Cos(angle)) * factor}
	}
	for i, a := range cursorOutline {
		b := cursorOutline[(i+1)%len(cursorOutline)]
		p0, p3 := a[0], b[0]
		p1, p2 := cursorPoint{p0.x + a[2].x, p0.y + a[2].y}, cursorPoint{p3.x + b[1].x, p3.y + b[1].y}
		for step := 0; step < 16; step++ {
			t := float64(step) / 16
			u := 1 - t
			points = append(points, transform(cursorPoint{u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x, u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y}))
		}
	}
	return points
}

func cursorDistance(x, y float64, outline []cursorPoint) float64 {
	inside := false
	nearest := math.Inf(1)
	for i, a := range outline {
		b := outline[(i+1)%len(outline)]
		dx, dy := b.x-a.x, b.y-a.y
		length := dx*dx + dy*dy
		t := 0.0
		if length > 0 {
			t = clampCursor(((x-a.x)*dx + (y-a.y)*dy) / length)
		}
		ox, oy := x-a.x-t*dx, y-a.y-t*dy
		nearest = math.Min(nearest, ox*ox+oy*oy)
		if (a.y > y) != (b.y > y) && x < (b.x-a.x)*(y-a.y)/(b.y-a.y)+a.x {
			inside = !inside
		}
	}
	distance := math.Sqrt(nearest)
	if inside {
		return -distance
	}
	return distance
}

// Cache curved, antialiased artwork rather than rasterizing vector paths on
// every animation tick. The hotspot stays fixed through the press squish.
func makeCursorGlyph(scale float64, failed bool) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, cursorTileSize, cursorTileSize))
	outline := cursorCurve(scale)
	accent := cursorAccent(failed)
	distances := make([]float64, cursorTileSize*cursorTileSize)
	for y := 0; y < cursorTileSize; y++ {
		for x := 0; x < cursorTileSize; x++ {
			distances[y*cursorTileSize+x] = cursorDistance(float64(x-cursorTileAnchor)+.5, float64(y-cursorTileAnchor)+.5, outline)
		}
	}
	for y := 0; y < cursorTileSize; y++ {
		for x := 0; x < cursorTileSize; x++ {
			d := distances[y*cursorTileSize+x]
			if y >= 2 {
				shadow := math.Max(0, distances[(y-2)*cursorTileSize+x])
				blendCursor(canvas, x, y, color.NRGBA{R: 20, G: 41, B: 61, A: 255}, .11*math.Exp(-shadow*shadow/8))
			}
			for _, glow := range [][2]float64{{44, .02}, {36, .024}, {29, .03}, {23, .038}, {18, .048}, {14, .06}, {10, .075}, {7, .095}} {
				coverage := clampCursor(glow[0]*42/128*scale/2 + .5 - math.Max(d, 0))
				blendCursor(canvas, x, y, accent, coverage*glow[1])
			}
			blendCursor(canvas, x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, clampCursor(5*42.0/128*scale/2+.5-d))
			blendCursor(canvas, x, y, accent, clampCursor(.5-d))
		}
	}
	return canvas
}

func cursorRing(target *image.RGBA, radius, opacity float64, accent color.NRGBA) {
	for y := 0; y < cursorTileSize; y++ {
		for x := 0; x < cursorTileSize; x++ {
			distance := math.Hypot(float64(x-cursorTileAnchor)+.5, float64(y-cursorTileAnchor)+.5)
			coverage := clampCursor(1.6 - math.Abs(distance-radius))
			blendCursor(target, x, y, accent, coverage*opacity)
		}
	}
}

func cursorTrail(target *image.RGBA, heading cursorPoint, opacity float64, accent color.NRGBA) {
	length := math.Hypot(heading.x, heading.y)
	if length < 1 {
		return
	}
	dx, dy := heading.x/length, heading.y/length
	// Cua's trail is anchored behind the arrow body, clear of its tip.
	end := cursorPoint{cursorTileAnchor + 2, cursorTileAnchor + 11.9}
	start := cursorPoint{end.x - dx*27, end.y - dy*27}
	for y := 0; y < cursorTileSize; y++ {
		for x := 0; x < cursorTileSize; x++ {
			t := clampCursor(((float64(x)+.5-start.x)*dx + (float64(y)+.5-start.y)*dy) / 27)
			cx, cy := start.x+t*27*dx, start.y+t*27*dy
			d := math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy)
			coverage := clampCursor(.5 + 1.7*t - d)
			blendCursor(target, x, y, accent, opacity*t*t*coverage)
		}
	}
}

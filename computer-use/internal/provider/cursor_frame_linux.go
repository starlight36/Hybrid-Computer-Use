//go:build linux

package provider

import (
	"image"
	"image/draw"
	"math"
	"time"

	"github.com/jezek/xgb/xproto"
)

const cursorIdleTime = 1500 * time.Millisecond
const cursorMoveTime = 140 * time.Millisecond
const cursorPulseTime = 550 * time.Millisecond

type cursorPoint struct{ x, y float64 }

type cursorVisual struct {
	from, to             cursorPoint
	started, last, pulse time.Time
	placed, pressed      bool
	failed               bool
	rect                 []float64
}

func (v *cursorVisual) position(now time.Time) cursorPoint {
	t := math.Max(0, math.Min(1, float64(now.Sub(v.started))/float64(cursorMoveTime)))
	t = t * t * (3 - 2*t)
	return cursorPoint{v.from.x + (v.to.x-v.from.x)*t, v.from.y + (v.to.y-v.from.y)*t}
}

func (v *cursorVisual) apply(event cursorEvent, now time.Time) {
	next := cursorPoint{event.X, event.Y}
	if !v.placed {
		v.from, v.to, v.started = next, next, now
	} else if next != v.to {
		v.from = v.position(now)
		v.to, v.started = next, now
	}
	v.last, v.placed = now, true
	v.rect = event.Rect
	v.failed = event.Kind == "error"
	if event.Kind == "press" {
		// A click cue belongs at the emitted input's target, even when a
		// decorative move has not caught up. Never delay input for animation.
		v.from, v.started = v.to, now.Add(-cursorMoveTime)
		v.pressed = true
		v.pulse = now
	}
	if event.Kind == "release" || event.Kind == "end" || event.Kind == "error" {
		v.pressed = false
	}
}

type cursorPaint struct {
	bounds     image.Rectangle
	pixels     []byte
	highlights []xproto.Rectangle
}

// cursorPixels returns a small premultiplied BGRA tile and clipped target edges.
// The desktop compositor blends the soft outline, glow and animated effects.
func cursorPixels(v cursorVisual, now time.Time, width, height int) cursorPaint {
	p := v.position(now)
	x, y := int(math.Round(p.x)), int(math.Round(p.y))
	tile := image.NewRGBA(image.Rect(0, 0, cursorTileSize, cursorTileSize))
	accent := cursorAccent(v.failed)
	elapsed := now.Sub(v.started)
	if elapsed >= 0 && elapsed < cursorMoveTime {
		progress := float64(elapsed) / float64(cursorMoveTime)
		cursorTrail(tile, cursorPoint{v.to.x - v.from.x, v.to.y - v.from.y}, .22*math.Sin(math.Pi*progress), accent)
	}
	age := now.Sub(v.pulse)
	if !v.pulse.IsZero() && age >= 0 && age < cursorPulseTime {
		t := float64(age) / float64(cursorPulseTime)
		cursorRing(tile, 11+17*(1-(1-t)*(1-t)), .74*(1-t)*(1-.35*t), accent)
	}
	draw.Draw(tile, tile.Rect, cursorGlyph(v.pressed, v.failed), image.Point{}, draw.Over)
	opacity := clampCursor(float64(cursorIdleTime-now.Sub(v.last)) / float64(300*time.Millisecond))
	origin := image.Pt(x-cursorTileAnchor, y-cursorTileAnchor)
	desktop := image.Rect(0, 0, width, height)
	crop := cursorInkBounds(tile).Add(origin).Intersect(desktop)
	paint := cursorPaint{bounds: crop}
	if !crop.Empty() {
		paint.pixels = make([]byte, crop.Dx()*crop.Dy()*4)
		for py := crop.Min.Y; py < crop.Max.Y; py++ {
			for px := crop.Min.X; px < crop.Max.X; px++ {
				source := tile.PixOffset(px-origin.X, py-origin.Y)
				dest := ((py-crop.Min.Y)*crop.Dx() + px - crop.Min.X) * 4
				for channel, index := range [4]int{2, 1, 0, 3} {
					paint.pixels[dest+channel] = uint8(math.Round(float64(tile.Pix[source+index]) * opacity))
				}
			}
		}
	}
	if len(v.rect) == 4 {
		r := image.Rect(int(v.rect[0]), int(v.rect[1]), int(v.rect[0]+v.rect[2]), int(v.rect[1]+v.rect[3])).Intersect(desktop)
		if !r.Empty() {
			// The strips do not overlap, even for one-pixel targets.
			edges := []image.Rectangle{image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1)}
			if r.Dy() > 1 {
				edges = append(edges, image.Rect(r.Min.X, r.Max.Y-1, r.Max.X, r.Max.Y))
			}
			if r.Dy() > 2 {
				edges = append(edges, image.Rect(r.Min.X, r.Min.Y+1, r.Min.X+1, r.Max.Y-1))
				if r.Dx() > 1 {
					edges = append(edges, image.Rect(r.Max.X-1, r.Min.Y+1, r.Max.X, r.Max.Y-1))
				}
			}
			for _, edge := range edges {
				paint.highlights = append(paint.highlights, cursorXRectangle(edge))
				// Preserve the original outline pixels where a strip crosses
				// the cursor, rather than blending two translucent layers.
				overlap := edge.Intersect(crop)
				for y := overlap.Min.Y; y < overlap.Max.Y; y++ {
					start := ((y-crop.Min.Y)*crop.Dx() + overlap.Min.X - crop.Min.X) * 4
					clear(paint.pixels[start : start+overlap.Dx()*4])
				}
			}
		}
	}
	return paint
}

func cursorInkBounds(tile *image.RGBA) image.Rectangle {
	bounds := image.Rectangle{Min: tile.Rect.Max, Max: tile.Rect.Min}
	for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
		row := tile.Pix[tile.PixOffset(tile.Rect.Min.X, y):]
		for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
			if row[(x-tile.Rect.Min.X)*4+3] != 0 {
				bounds.Min.X = min(bounds.Min.X, x)
				bounds.Min.Y = min(bounds.Min.Y, y)
				bounds.Max.X = max(bounds.Max.X, x+1)
				bounds.Max.Y = max(bounds.Max.Y, y+1)
			}
		}
	}
	return bounds
}

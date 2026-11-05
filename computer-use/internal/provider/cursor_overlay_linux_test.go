//go:build linux

package provider

import (
	"encoding/json"
	"image"
	"math"
	"os"
	"testing"
	"time"
)

func TestCursorWireFixtures(t *testing.T) {
	data, err := os.ReadFile("../../testdata/provider-wire/cursor-events.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Events []cursorEvent `json:"events"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Events) != 9 {
		t.Fatal("missing cursor wire events")
	}
	for _, event := range fixture.Events {
		if err = event.validate(); err != nil {
			t.Fatalf("%s: %v", event.Kind, err)
		}
	}
	for _, event := range []cursorEvent{
		{SchemaVersion: 2, Kind: "move"},
		{SchemaVersion: 1, Kind: "capture_begin"},
		{SchemaVersion: 1, Kind: "move", X: math.NaN()},
		{SchemaVersion: 1, Kind: "target", Rect: []float64{1, 2, 0, 4}},
		{SchemaVersion: 1, Kind: "target", Rect: []float64{1, 2, 3}},
		{SchemaVersion: 1, Kind: "type_text"},
	} {
		if event.validate() == nil {
			t.Fatalf("accepted invalid event: %+v", event)
		}
	}
}

func TestCursorMotionRetargetsWithoutJumping(t *testing.T) {
	now := time.Now()
	v := cursorVisual{}
	v.apply(cursorEvent{Kind: "move", X: 10, Y: 20}, now)
	v.apply(cursorEvent{Kind: "move", X: 1010, Y: 1020}, now)
	half := now.Add(cursorMoveTime / 2)
	before := v.position(half)
	if before.x != 510 || before.y != 520 {
		t.Fatalf("halfway pose: %+v", before)
	}
	v.apply(cursorEvent{Kind: "move", X: 40, Y: 30}, half)
	if after := v.position(half); after != before {
		t.Fatalf("retarget jumped: %+v -> %+v", before, after)
	}
	if end := v.position(half.Add(cursorMoveTime)); end != (cursorPoint{40, 30}) {
		t.Fatalf("final pose: %+v", end)
	}
	v.apply(cursorEvent{Kind: "press", X: 40, Y: 30}, half)
	if p := v.position(half); p != (cursorPoint{40, 30}) {
		t.Fatalf("click cue was drawn away from its input target: %+v", p)
	}
	v.apply(cursorEvent{Kind: "error", X: 40, Y: 30}, half)
	if v.pressed || !v.failed {
		t.Fatal("failed action left the cursor pressed")
	}
}

func TestCursorPixelsStayWithinDisplay(t *testing.T) {
	now := time.Now()
	for _, point := range []cursorPoint{{0, 0}, {319, 199}, {-30, -30}, {350, 220}} {
		v := cursorVisual{from: point, to: point, started: now, last: now, placed: true, pulse: now, rect: []float64{-5, -5, 400, 250}}
		paint := cursorPixels(v, now, 320, 200)
		if !paint.bounds.Empty() && !paint.bounds.In(image.Rect(0, 0, 320, 200)) {
			t.Fatalf("unclipped tile: %v", paint.bounds)
		}
		if len(paint.pixels) != paint.bounds.Dx()*paint.bounds.Dy()*4 {
			t.Fatal("invalid ARGB tile length")
		}
		for _, r := range paint.highlights {
			if r.X < 0 || r.Y < 0 || r.Width == 0 || r.Height == 0 || int(r.X)+int(r.Width) > 320 || int(r.Y)+int(r.Height) > 200 {
				t.Fatalf("unclipped cursor rectangle: %+v", r)
			}
		}
		if len(paint.highlights) == 0 {
			t.Fatal("target highlight missing")
		}
	}
}

func TestCursorArtworkPreservesTransparentAntialiasedPixels(t *testing.T) {
	for _, pressed := range []bool{false, true} {
		glyph := cursorGlyph(pressed, false)
		partial, opaque, transparent := 0, 0, 0
		for i := 0; i < len(glyph.Pix); i += 4 {
			a := glyph.Pix[i+3]
			if a == 0 {
				transparent++
			} else if a == 255 {
				opaque++
			} else {
				partial++
			}
			if glyph.Pix[i] > a || glyph.Pix[i+1] > a || glyph.Pix[i+2] > a {
				t.Fatal("non-premultiplied edge would produce a dark or bright fringe")
			}
		}
		if partial < 100 || opaque < 100 || transparent < 100 {
			t.Fatalf("lost transparency or artwork: partial=%d opaque=%d transparent=%d", partial, opaque, transparent)
		}
	}
}

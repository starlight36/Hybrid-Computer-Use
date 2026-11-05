//go:build linux

package provider

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// This opt-in test draws on the signed-in desktop. Ordinary go test is hermetic.
func TestCursorDesktopCaptureAndInputIsolation(t *testing.T) {
	if os.Getenv("COMPUTER_USE_CURSOR_DESKTOP_TEST") != "1" {
		t.Skip("requires a disposable X11 desktop with a compositing manager")
	}
	observer, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	screen := xproto.Setup(observer).DefaultScreen(observer)
	grab := func() []byte {
		t.Helper()
		image, err := xproto.GetImage(observer, xproto.ImageFormatZPixmap, xproto.Drawable(screen.Root), 0, 0, screen.WidthInPixels, screen.HeightInPixels, 0xffffffff).Reply()
		if err != nil {
			t.Fatal(err)
		}
		return image.Data
	}
	focus, err := xproto.GetInputFocus(observer).Reply()
	if err != nil {
		t.Fatal(err)
	}
	pointer, err := xproto.QueryPointer(observer, screen.Root).Reply()
	if err != nil {
		t.Fatal(err)
	}
	baseline := grab()
	cursor, child, err := startLinuxCursorOverlay(append(os.Environ(), "COMPUTER_USE_CURSOR=1"))
	if err != nil || cursor == nil {
		t.Fatalf("start cursor: %v", err)
	}
	defer cursor.close()
	connection, err := net.FileConn(child)
	_ = child.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	encoder := json.NewEncoder(connection)
	decoder := json.NewDecoder(connection)
	send := func(event cursorEvent) {
		t.Helper()
		event.SchemaVersion = 1
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	barrier := func(kind string, sequence uint64) {
		t.Helper()
		send(cursorEvent{Kind: kind, Sequence: sequence})
		var ack struct {
			SchemaVersion int    `json:"schema_version"`
			Sequence      uint64 `json:"sequence"`
			OK            bool   `json:"ok"`
		}
		if err := decoder.Decode(&ack); err != nil {
			t.Fatal(err)
		}
		if ack.SchemaVersion != 1 || ack.Sequence != sequence || !ack.OK {
			t.Fatalf("bad capture ack: %+v", ack)
		}
	}
	// The cursor covers the preferred witness position. The fence must sample
	// another point rather than treating the old cue as the background.
	send(cursorEvent{Kind: "move", X: float64(screen.WidthInPixels - 1), Y: float64(screen.HeightInPixels - 1)})
	deadline := time.Now().Add(2 * time.Second)
	for bytes.Equal(baseline, grab()) {
		if time.Now().After(deadline) {
			t.Fatal("cursor is absent from external desktop capture")
		}
		time.Sleep(20 * time.Millisecond)
	}
	barrier("capture_begin", 1)
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("cursor pixels leaked into the excluded capture")
	}
	// The render loop must not repaint while a capture is held.
	send(cursorEvent{Kind: "press", X: 600, Y: 400})
	time.Sleep(100 * time.Millisecond)
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("cursor repainted during capture exclusion")
	}
	barrier("capture_end", 1)
	time.Sleep(100 * time.Millisecond)
	if bytes.Equal(baseline, grab()) {
		t.Fatal("cursor not restored for external recording")
	}
	afterFocus, err := xproto.GetInputFocus(observer).Reply()
	if err != nil {
		t.Fatal(err)
	}
	afterPointer, err := xproto.QueryPointer(observer, screen.Root).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if focus.Focus != afterFocus.Focus || pointer.RootX != afterPointer.RootX || pointer.RootY != afterPointer.RootY {
		t.Fatal("cursor rendering changed keyboard focus or the real pointer")
	}
	// Closing the provider's side channel tears down its desktop window.
	_ = connection.Close()
	time.Sleep(150 * time.Millisecond)
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("cursor window survived provider disconnect")
	}
}

func TestCursorDesktopLargeOutlineUsesSmallWindows(t *testing.T) {
	grab := cursorTestGrab(t)
	baseline := grab()
	observer, err := openCursorX11(os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	width, height := observer.screen.WidthInPixels, observer.screen.HeightInPixels
	controller, writer := cursorTestClient(t, true)
	cursorTestSend(t, writer, cursorEvent{
		Kind: "target", X: float64(width / 2), Y: float64(height / 2),
		Rect: []float64{0, 0, float64(width), float64(height)},
	})
	checkVisible := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			frame := grab()
			midX, midY := int(width)/2, int(height)/2
			// Check the center cursor and all four distant outline strips.
			pixels := [][2]int{{midX, midY}, {midX, 0}, {midX, int(height) - 1}, {0, midY}, {int(width) - 1, midY}}
			visible := true
			for _, p := range pixels {
				index := (p[1]*int(width) + p[0]) * 4
				visible = visible && !bytes.Equal(baseline[index:index+4], frame[index:index+4])
			}
			if visible {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("external capture missed the cursor or one of the outline strips")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	checkVisible()
	windows, err := observer.ownedWindows()
	if err != nil {
		t.Fatal(err)
	}
	var area int
	for window, overlay := range windows {
		if !overlay {
			continue
		}
		geometry, err := xproto.GetGeometry(observer.conn, xproto.Drawable(window)).Reply()
		if err != nil {
			t.Fatal(err)
		}
		area += int(geometry.Width) * int(geometry.Height)
	}
	if maxArea := cursorTileSize*cursorTileSize + 2*(int(width)+int(height)); area > maxArea {
		t.Fatalf("allocated %d overlay pixels for a cursor and outline, want at most %d", area, maxArea)
	}
	_, reader := cursorTestClient(t, false)
	if !cursorTestBarrier(t, reader, "capture_begin", 1) {
		t.Fatal("reader could not exclude all outline windows")
	}
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("cursor or outline leaked into the screenshot")
	}
	if !cursorTestBarrier(t, reader, "capture_end", 1) {
		t.Fatal("capture did not release")
	}
	checkVisible()
	controller.shutdown()
	if !cursorTestBarrier(t, reader, "capture_begin", 2) {
		t.Fatal("reader could not fence the retired outline")
	}
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("retired outline left visible pixels after disconnect")
	}
	if !cursorTestBarrier(t, reader, "capture_end", 2) {
		t.Fatal("capture did not release")
	}
}

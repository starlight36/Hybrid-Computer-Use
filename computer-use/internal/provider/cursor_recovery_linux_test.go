//go:build linux

package provider

import (
	"bytes"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
)

// Pause a real X11 request before it is written to the server. The timeout
// also releases the gate if a regression blocks the test's external reader.
type cursorRequestGate struct {
	net.Conn
	match   func([]byte) bool
	armed   atomic.Bool
	reached chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (g *cursorRequestGate) Write(data []byte) (int, error) {
	if g.armed.Load() && g.match(data) && g.armed.Swap(false) {
		close(g.reached)
		select {
		case <-g.resume:
		case <-time.After(700 * time.Millisecond):
		}
	}
	return g.Conn.Write(data)
}

func (g *cursorRequestGate) release() {
	g.once.Do(func() { close(g.resume) })
}

func cursorGatedConnection(t *testing.T) (*cursorX11, *cursorRequestGate) {
	t.Helper()
	endpoint, err := parseCursorDisplay(os.Getenv("DISPLAY"))
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := cursorCookie(os.Environ(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.DialTimeout(endpoint.network, endpoint.address, cursorIOTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	gate := &cursorRequestGate{Conn: raw, reached: make(chan struct{}), resume: make(chan struct{})}
	_ = gate.SetDeadline(time.Now().Add(cursorIOTimeout))
	conn, err := xgb.NewConnNetWithCookieHex(gate, cookie)
	if err != nil {
		t.Fatal(err)
	}
	conn.DefaultScreen = endpoint.screen
	screen := xproto.Setup(conn).DefaultScreen(conn)
	x := &cursorX11{conn: conn, transport: gate, screen: screen, root: screen.Root}
	t.Cleanup(x.close)
	t.Cleanup(gate.release)
	if err = x.initializeCapture(); err != nil {
		t.Fatal(err)
	}
	_ = gate.SetDeadline(time.Time{})
	return x, gate
}

func cursorAwaitGate(t *testing.T, g *cursorRequestGate) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(2 * time.Second):
		t.Fatal("gated X11 request did not arrive")
	}
}

func cursorAwaitDraw(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("gated X11 operation did not finish")
		return nil
	}
}

func TestCursorDesktopStagesClippedFrameBeforeMoving(t *testing.T) {
	grab := cursorTestGrab(t)
	baseline := grab()
	x, gate := cursorGatedConnection(t)
	_ = gate.SetDeadline(time.Now().Add(cursorIOTimeout))
	if err := x.initializeOverlay(); err != nil {
		t.Fatal(err)
	}
	_ = gate.SetDeadline(time.Time{})
	var visual cursorVisual
	now := time.Now()
	visual.apply(cursorEvent{Kind: "press", X: float64(x.screen.WidthInPixels - 2), Y: float64(x.screen.HeightInPixels - 2)}, now)
	if err := x.draw(visual, now); err != nil {
		t.Fatal(err)
	}
	cursorTestVisible(t, grab, baseline)
	previous := grab()

	now = now.Add(time.Millisecond)
	visual.apply(cursorEvent{Kind: "press", X: float64(x.screen.WidthInPixels / 2), Y: float64(x.screen.HeightInPixels / 2)}, now)
	gate.match = func(data []byte) bool { return len(data) > 0 && data[0] == 72 } // PutImage
	gate.armed.Store(true)
	done := make(chan error, 1)
	go func() { done <- x.draw(visual, now) }()
	cursorAwaitGate(t, gate)
	// Give the compositor several refreshes with the upload still pending.
	time.Sleep(65 * time.Millisecond)
	if !bytes.Equal(previous, grab()) {
		t.Fatal("visible clipped frame changed before the replacement upload finished")
	}
	gate.release()
	if err := cursorAwaitDraw(t, done); err != nil {
		t.Fatal(err)
	}
	cursorWaitFor(t, "replacement frame", func() bool {
		frame := grab()
		return !bytes.Equal(previous, frame) && !bytes.Equal(baseline, frame)
	})
	x.close()
	_, reader := cursorTestClient(t, false)
	if !cursorTestBarrier(t, reader, "capture_begin", 1) || !bytes.Equal(baseline, grab()) || !cursorTestBarrier(t, reader, "capture_end", 1) {
		t.Fatal("retired cursor left pixels after the move")
	}
}

func TestCursorDesktopRestoresInterruptedOutline(t *testing.T) {
	grab := cursorTestGrab(t)
	baseline := grab()
	x, gate := cursorGatedConnection(t)
	if err := shape.Init(x.conn); err != nil {
		t.Fatal(err)
	}
	opcode := x.conn.Extensions["SHAPE"]
	width, height := float64(x.screen.WidthInPixels), float64(x.screen.HeightInPixels)
	controller, writer := cursorTestClient(t, true)
	cursorTestSend(t, writer, cursorEvent{
		Kind: "target", X: width / 2, Y: height / 2, Rect: []float64{0, 0, width, height},
	})
	cursorTestVisible(t, grab, baseline)
	time.Sleep(cursorMoveTime + 50*time.Millisecond)
	previous := grab()
	// Interrupt the second empty bounding-shape request. The first layer
	// has already been clipped and the writer's ordinary animation is idle.
	clips := 0
	gate.match = func(data []byte) bool {
		if len(data) != 16 || data[0] != opcode || data[1] != 1 || data[5] != byte(shape.SkBounding) {
			return false
		}
		clips++
		return clips == 2
	}
	gate.armed.Store(true)
	done := make(chan error, 1)
	go func() { done <- x.beginCapture() }()
	cursorAwaitGate(t, gate)
	x.transport.Close()
	gate.release()
	if err := cursorAwaitDraw(t, done); err == nil {
		t.Fatal("interrupted capture succeeded")
	}
	deadline := time.Now().Add(350 * time.Millisecond)
	for !bytes.Equal(previous, grab()) {
		if time.Now().After(deadline) {
			t.Fatal("writer did not restore the complete cue without another action")
		}
		time.Sleep(5 * time.Millisecond)
	}
	controller.shutdown()
	_, reader := cursorTestClient(t, false)
	if !cursorTestBarrier(t, reader, "capture_begin", 1) || !bytes.Equal(baseline, grab()) || !cursorTestBarrier(t, reader, "capture_end", 1) {
		t.Fatal("a fresh reader could not capture after the interrupted lease")
	}
}

func TestCursorDesktopFenceIgnoresOldCornerPixel(t *testing.T) {
	compositor := cursorFaultProcess(t, "picom")
	grab := cursorTestGrab(t)
	baseline := grab()
	observer, err := openCursorX11(os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	width, height := observer.screen.WidthInPixels, observer.screen.HeightInPixels
	controller, writer := cursorTestClient(t, true)
	cursorTestSend(t, writer, cursorEvent{Kind: "press", X: float64(width - 1), Y: float64(height - 1)})
	cursorWaitFor(t, "old cue at the probe pixel", func() bool {
		frame := grab()
		return !bytes.Equal(baseline[len(frame)-4:], frame[len(frame)-4:])
	})
	if err := compositor.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	defer compositor.Signal(syscall.SIGCONT)
	cursorTestSend(t, writer, cursorEvent{Kind: "press", X: float64(width / 2), Y: float64(height / 2)})
	cursorWaitFor(t, "new cursor geometry", func() bool {
		windows, err := observer.ownedWindows()
		if err != nil {
			t.Fatal(err)
		}
		for window, regular := range windows {
			if regular {
				g, err := xproto.GetGeometry(observer.conn, xproto.Drawable(window)).Reply()
				return err == nil && g.X >= int16(width/4) && g.X < int16(3*int(width)/4)
			}
		}
		return false
	})
	_, reader := cursorTestClient(t, false)
	cursorTestSend(t, reader, cursorEvent{Kind: "capture_begin", Sequence: 1})
	cursorWaitFor(t, "mapped compositor witness", func() bool {
		windows, err := observer.ownedWindows()
		if err != nil {
			t.Fatal(err)
		}
		for window, regular := range windows {
			if !regular {
				attributes, err := xproto.GetWindowAttributes(observer.conn, window).Reply()
				return err == nil && attributes.MapState == xproto.MapStateViewable
			}
		}
		return false
	})
	if err := compositor.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if !cursorTestAck(t, reader, 1) || !bytes.Equal(baseline, grab()) {
		t.Fatal("old corner pixel prevented a clean capture after compositor resumed")
	}
	controller.shutdown()
	if !cursorTestBarrier(t, reader, "capture_end", 1) {
		t.Fatal("capture release failed")
	}
}

func cursorWaitFor(t *testing.T, label string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

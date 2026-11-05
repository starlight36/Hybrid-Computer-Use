//go:build linux

package provider

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func requireCursorDesktop(t *testing.T) {
	t.Helper()
	if os.Getenv("COMPUTER_USE_CURSOR_DESKTOP_TEST") != "1" {
		t.Skip("requires a disposable X11 desktop")
	}
}

func cursorTestClient(t *testing.T, enabled bool) (*linuxCursorOverlay, net.Conn) {
	t.Helper()
	requireCursorDesktop(t)
	flag := "0"
	if enabled {
		flag = "1"
	}
	controller, child, err := startLinuxCursorOverlay(append(os.Environ(), "COMPUTER_USE_CURSOR="+flag))
	if err != nil || controller == nil {
		t.Fatalf("start: %v", err)
	}
	connection, err := net.FileConn(child)
	child.Close()
	if err != nil {
		controller.shutdown()
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close(); controller.shutdown() })
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	return controller, connection
}

func cursorTestSend(t *testing.T, connection net.Conn, event cursorEvent) {
	t.Helper()
	event.SchemaVersion = 1
	if err := json.NewEncoder(connection).Encode(event); err != nil {
		t.Fatal(err)
	}
}

func cursorTestBarrier(t *testing.T, connection net.Conn, kind string, sequence uint64) bool {
	t.Helper()
	cursorTestSend(t, connection, cursorEvent{Kind: kind, Sequence: sequence})
	return cursorTestAck(t, connection, sequence)
}

func cursorTestAck(t *testing.T, connection net.Conn, sequence uint64) bool {
	t.Helper()
	var ack struct {
		SchemaVersion int    `json:"schema_version"`
		Sequence      uint64 `json:"sequence"`
		OK            bool   `json:"ok"`
	}
	if err := json.NewDecoder(connection).Decode(&ack); err != nil {
		t.Fatal(err)
	}
	if ack.SchemaVersion != 1 || ack.Sequence != sequence {
		t.Fatalf("wrong acknowledgement: %+v", ack)
	}
	return ack.OK
}

func cursorTestGrab(t *testing.T) func() []byte {
	t.Helper()
	requireCursorDesktop(t)
	observer, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	screen := xproto.Setup(observer).DefaultScreen(observer)
	return func() []byte {
		t.Helper()
		image, err := xproto.GetImage(observer, xproto.ImageFormatZPixmap, xproto.Drawable(screen.Root), 0, 0, screen.WidthInPixels, screen.HeightInPixels, 0xffffffff).Reply()
		if err != nil {
			t.Fatal(err)
		}
		return image.Data
	}
}

func cursorTestVisible(t *testing.T, grab func() []byte, baseline []byte) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for bytes.Equal(baseline, grab()) {
		if time.Now().After(deadline) {
			t.Fatal("cursor did not become visible")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCursorDesktopExcludesAnotherController(t *testing.T) {
	grab := cursorTestGrab(t)
	baseline := grab()
	_, writer := cursorTestClient(t, true)
	cursorTestSend(t, writer, cursorEvent{Kind: "move", X: 350, Y: 350})
	cursorTestVisible(t, grab, baseline)
	_, reader := cursorTestClient(t, false)
	if !cursorTestBarrier(t, reader, "capture_begin", 1) {
		t.Fatal("reader could not exclude the writer")
	}
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("another controller's cursor contaminated the screenshot")
	}
	// A writer cannot remap its overlay while another process holds capture.
	cursorTestSend(t, writer, cursorEvent{Kind: "press", X: 600, Y: 400})
	time.Sleep(50 * time.Millisecond)
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("writer painted through the global capture lease")
	}
	if !cursorTestBarrier(t, reader, "capture_end", 1) {
		t.Fatal("capture did not release")
	}
}

func TestCursorDesktopDisconnectReleasesCaptureLease(t *testing.T) {
	grab := cursorTestGrab(t)
	baseline := grab()
	_, writer := cursorTestClient(t, true)
	cursorTestSend(t, writer, cursorEvent{Kind: "move", X: 350, Y: 350})
	cursorTestVisible(t, grab, baseline)
	reader, connection := cursorTestClient(t, false)
	if !cursorTestBarrier(t, connection, "capture_begin", 1) {
		t.Fatal("capture failed")
	}
	reader.shutdown()
	cursorTestSend(t, writer, cursorEvent{Kind: "move", X: 550, Y: 450})
	cursorTestVisible(t, grab, baseline)
	_, fresh := cursorTestClient(t, false)
	if !cursorTestBarrier(t, fresh, "capture_begin", 1) {
		t.Fatal("dead reader retained the capture lease")
	}
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("cursor survived exclusion after reader loss")
	}
	if !cursorTestBarrier(t, fresh, "capture_end", 1) {
		t.Fatal("capture release failed")
	}
}

func cursorFaultProcess(t *testing.T, name string) *os.Process {
	t.Helper()
	requireCursorDesktop(t)
	if os.Getenv("COMPUTER_USE_CURSOR_FAULT_TEST") != "1" {
		t.Skip("requires opt-in desktop process suspension")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var matches []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		path := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(path)
		if err != nil || !FileOwnedByUID(info, os.Geteuid()) {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join(path, "comm"))
		if strings.TrimSpace(string(comm)) != name {
			continue
		}
		if name == "Xvfb" {
			arguments, _ := os.ReadFile(filepath.Join(path, "cmdline"))
			if bytes.Contains(append([]byte{0}, arguments...), []byte("\x00"+strings.Split(os.Getenv("DISPLAY"), ".")[0]+"\x00")) {
				matches = append(matches, pid)
			}
		} else {
			environment, _ := os.ReadFile(filepath.Join(path, "environ"))
			if bytes.Contains(append([]byte{0}, environment...), []byte("\x00DISPLAY="+os.Getenv("DISPLAY")+"\x00")) {
				matches = append(matches, pid)
			}
		}
	}
	if len(matches) != 1 {
		t.Skipf("need one %s owned by this user on this DISPLAY", name)
	}
	process, err := os.FindProcess(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func TestCursorDesktopRejectsStoppedCompositor(t *testing.T) {
	compositor := cursorFaultProcess(t, "picom")
	grab := cursorTestGrab(t)
	baseline := grab()
	controller, connection := cursorTestClient(t, true)
	cursorTestSend(t, connection, cursorEvent{Kind: "move", X: 350, Y: 350})
	cursorTestVisible(t, grab, baseline)
	defer compositor.Signal(syscall.SIGCONT)
	if err := compositor.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if cursorTestBarrier(t, connection, "capture_begin", 1) {
		t.Fatal("stopped compositor acknowledged a clean capture")
	}
	controller.shutdown()
	_ = compositor.Signal(syscall.SIGCONT)
	// A fresh observer must also fence the retired window after owner loss.
	_, fresh := cursorTestClient(t, false)
	if !cursorTestBarrier(t, fresh, "capture_begin", 1) {
		t.Fatal("capture did not recover after compositor resumed")
	}
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("retired cursor pixels survived recovery")
	}
	if !cursorTestBarrier(t, fresh, "capture_end", 1) {
		t.Fatal("release after recovery failed")
	}
}

func TestCursorDesktopCloseCancelsBlockedX11(t *testing.T) {
	server := cursorFaultProcess(t, "Xvfb")
	controller, connection := cursorTestClient(t, true)
	if !cursorTestBarrier(t, connection, "capture_begin", 1) || !cursorTestBarrier(t, connection, "capture_end", 1) {
		t.Fatal("initialization failed")
	}
	defer server.Signal(syscall.SIGCONT)
	if err := server.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	cursorTestSend(t, connection, cursorEvent{Kind: "move", X: 300, Y: 300})
	time.Sleep(30 * time.Millisecond)
	controller.close()
	for _, done := range []chan struct{}{controller.renderDone, controller.readerDone} {
		select {
		case <-done:
		case <-time.After(cursorIOTimeout):
			t.Fatal("close left an X11 goroutine blocked")
		}
	}
}

func TestCursorDesktopReclaimsDeadAndReusedLease(t *testing.T) {
	requireCursorDesktop(t)
	x, err := openCursorX11(os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	// A live but unrelated resource can reuse a dead owner's window id.
	other, err := xproto.NewWindowId(x.conn)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(x.conn, 0, other, x.root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, 0, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	stale := make([]byte, 20)
	binary.LittleEndian.PutUint32(stale, uint32(other))
	if err = xproto.ChangePropertyChecked(x.conn, xproto.PropModeReplace, x.root, x.owner, xproto.AtomCardinal, 32, 5, stale).Check(); err != nil {
		t.Fatal(err)
	}
	acquired, err := x.lockCapture(true)
	if err != nil || !acquired {
		t.Fatalf("stale lease prevented capture: %v", err)
	}
	x.unlockCapture()
}

func TestCursorDesktopExcludesFadingOutline(t *testing.T) {
	requireCursorDesktop(t)
	if os.Getenv("COMPUTER_USE_CURSOR_FADE_TEST") != "1" {
		t.Skip("requires a disposable compositor with instant fade-in and slow fade-out")
	}
	grab := cursorTestGrab(t)
	baseline := grab()
	controller, writer := cursorTestClient(t, true)
	observer, err := openCursorX11(os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	width, height := float64(observer.screen.WidthInPixels), float64(observer.screen.HeightInPixels)
	cursorTestSend(t, writer, cursorEvent{
		Kind: "target", X: width / 2, Y: height / 2, Rect: []float64{0, 0, width, height},
	})
	top := int(width/2) * 4
	outlineVisible := func() bool { return !bytes.Equal(baseline[top:top+4], grab()[top:top+4]) }
	cursorWaitFor(t, "outline before capture", outlineVisible)
	_, reader := cursorTestClient(t, false)
	if !cursorTestBarrier(t, reader, "capture_begin", 1) {
		t.Fatal("fading compositor could not exclude the outline")
	}
	if !bytes.Equal(baseline, grab()) {
		t.Fatal("successful capture contains fading overlay pixels")
	}
	if !cursorTestBarrier(t, reader, "capture_end", 1) {
		t.Fatal("capture release failed")
	}
	cursorWaitFor(t, "restored outline", outlineVisible)
	controller.shutdown()
	if !cursorTestBarrier(t, reader, "capture_begin", 2) || !bytes.Equal(baseline, grab()) || !cursorTestBarrier(t, reader, "capture_end", 2) {
		t.Fatal("retired outline contaminated a subsequent capture")
	}
}

func TestCursorDesktopDoesNotRemapReusedWindow(t *testing.T) {
	requireCursorDesktop(t)
	x, err := openCursorX11(os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	window, err := xproto.NewWindowId(x.conn)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(x.conn, 0, window, x.root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, 0, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	// Simulate a saved overlay id now occupied by an unrelated window.
	if acquired, err := x.lockCapture(true); err != nil || !acquired {
		t.Fatalf("capture lease: %v", err)
	}
	x.restore = []cursorHiddenWindow{{window: window}}
	if err := x.endCapture(); err != nil {
		t.Fatal(err)
	}
	attributes, err := xproto.GetWindowAttributes(x.conn, window).Reply()
	if err != nil || attributes.MapState != xproto.MapStateUnmapped {
		t.Fatalf("capture restored an unrelated window: %v, %v", attributes, err)
	}
}

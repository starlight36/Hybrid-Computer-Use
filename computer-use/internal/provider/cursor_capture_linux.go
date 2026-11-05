//go:build linux

package provider

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
)

const cursorOverlayMarker = "_HCU_CURSOR_OVERLAY"
const cursorPaintHistory = "_HCU_CURSOR_PAINTED"
const cursorCaptureMarker = "_HCU_CURSOR_HIDDEN_BY"

type cursorHiddenWindow struct {
	window xproto.Window
	shape  []xproto.Rectangle
}

// A server-side lease covers different processes, users and DISPLAY aliases.
// Its unmapped InputOnly owner window dies with the transport, allowing the
// next client to reclaim a lease even after an ungraceful owner exit.
func (x *cursorX11) lockCapture(wait bool) (bool, error) {
	deadline := time.Now().Add(cursorIOTimeout)
	for {
		_ = x.transport.SetDeadline(deadline)
		acquired, err := x.tryCaptureLease()
		_ = x.transport.SetDeadline(time.Time{})
		if err != nil || acquired {
			x.locked = acquired
			return acquired, err
		}
		if !wait {
			return false, nil
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("cursor capture is busy")
		}
		select {
		case <-x.stop:
			return false, fmt.Errorf("cursor closed")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (x *cursorX11) tryCaptureLease() (bool, error) {
	if err := xproto.GrabServerChecked(x.conn).Check(); err != nil {
		return false, err
	}
	defer func() { _ = xproto.UngrabServerChecked(x.conn).Check() }()
	property, err := xproto.GetProperty(x.conn, false, x.root, x.owner, xproto.AtomCardinal, 0, 5).Reply()
	if err != nil {
		return false, err
	}
	if len(property.Value) == 20 && !bytes.Equal(property.Value, x.leaseToken) {
		owner := xproto.Window(binary.LittleEndian.Uint32(property.Value))
		marker, err := xproto.GetProperty(x.conn, false, owner, x.owner, xproto.AtomCardinal, 0, 5).Reply()
		if err == nil && bytes.Equal(marker.Value, property.Value) {
			return false, nil
		}
		if err != nil {
			if _, gone := err.(xproto.WindowError); !gone {
				return false, err
			}
		}
	}
	if err = xproto.ChangePropertyChecked(x.conn, xproto.PropModeReplace, x.root, x.owner, xproto.AtomCardinal, 32, 5, x.leaseToken).Check(); err != nil {
		return false, err
	}
	return true, nil
}

func (x *cursorX11) unlockCapture() {
	if x.locked {
		_ = xproto.DeletePropertyChecked(x.conn, x.root, x.owner).Check()
		x.locked = false
	}
}

func (x *cursorX11) initializeCapture() error {
	var err error
	if x.lockWindow, err = xproto.NewWindowId(x.conn); err != nil {
		return err
	}
	if err = xproto.CreateWindowChecked(x.conn, 0, x.lockWindow, x.root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, 0, 0, nil).Check(); err != nil {
		return err
	}
	compositor := fmt.Sprintf("_NET_WM_CM_S%d", x.conn.DefaultScreen)
	for name, dest := range map[string]*xproto.Atom{cursorOverlayMarker: &x.marker, cursorPaintHistory: &x.history, cursorCaptureMarker: &x.hidden, "_HCU_CURSOR_CAPTURE_OWNER": &x.owner, compositor: &x.compositor} {
		atom, err := xproto.InternAtom(x.conn, false, uint16(len(name)), name).Reply()
		if err != nil {
			return err
		}
		*dest = atom.Atom
	}
	x.leaseToken = make([]byte, 20)
	binary.LittleEndian.PutUint32(x.leaseToken, uint32(x.lockWindow))
	if _, err = rand.Read(x.leaseToken[4:]); err != nil {
		return err
	}
	if err = xproto.ChangePropertyChecked(x.conn, xproto.PropModeReplace, x.lockWindow, x.owner, xproto.AtomCardinal, 32, 5, x.leaseToken).Check(); err != nil {
		return err
	}
	return nil
}

// Read each time so a running worker follows a compositor restart on its
// own root window. This query neither acquires a lease nor hides any cues.
func (x *cursorX11) captureIdentity() (string, error) {
	_ = x.transport.SetDeadline(time.Now().Add(cursorIOTimeout))
	defer x.transport.SetDeadline(time.Time{})
	if x.captureSession == 0 {
		const name = "_HCU_CLEAN_CAPTURE_SESSION"
		atom, err := xproto.InternAtom(x.conn, true, uint16(len(name)), name).Reply()
		if err != nil {
			return "", err
		}
		x.captureSession = atom.Atom
		if x.captureSession == 0 {
			return "", nil
		}
	}
	property, err := xproto.GetProperty(x.conn, false, x.root, x.captureSession, xproto.AtomString, 0, 5).Reply()
	if err != nil {
		return "", err
	}
	if property.Type != xproto.AtomString || property.Format != 8 || len(property.Value) != 16 || property.BytesAfter != 0 {
		return "", nil
	}
	return hex.EncodeToString(property.Value), nil
}

func (x *cursorX11) setPainted(painted bool) error {
	value := uint32(0)
	if painted {
		value = 1
	}
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, value)
	return xproto.ChangePropertyChecked(x.conn, xproto.PropModeReplace, x.root, x.history, xproto.AtomCardinal, 32, 1, data).Check()
}

func (x *cursorX11) painted() (bool, error) {
	value, err := xproto.GetProperty(x.conn, false, x.root, x.history, xproto.AtomCardinal, 0, 1).Reply()
	if err != nil {
		return false, err
	}
	return len(value.Value) == 4 && binary.LittleEndian.Uint32(value.Value) != 0, nil
}

func (x *cursorX11) windowKind(window xproto.Window) (uint32, error) {
	property, err := xproto.GetProperty(x.conn, false, window, x.marker, xproto.AtomCardinal, 0, 1).Reply()
	if _, gone := err.(xproto.WindowError); gone {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(property.Value) != 4 {
		return 0, nil
	}
	return binary.LittleEndian.Uint32(property.Value), nil
}

func (x *cursorX11) ownedWindows() (map[xproto.Window]bool, error) {
	tree, err := xproto.QueryTree(x.conn, x.root).Reply()
	if err != nil {
		return nil, err
	}
	windows := make(map[xproto.Window]bool)
	for _, window := range tree.Children {
		kind, err := x.windowKind(window)
		if err != nil {
			return nil, err
		}
		if kind == 1 || kind == 2 {
			windows[window] = kind == 1
		}
	}
	return windows, nil
}

// Make a short group of window changes indivisible to other X11 clients.
// Never hold the grab while uploading a frame or waiting for the compositor.
func (x *cursorX11) withServerGrab(change func() error) (err error) {
	xproto.GrabServer(x.conn)
	defer func() {
		if release := xproto.UngrabServerChecked(x.conn).Check(); err == nil {
			err = release
		}
	}()
	return change()
}

func (x *cursorX11) beginCapture() error {
	if x.locked {
		return fmt.Errorf("capture already held")
	}
	if _, err := x.lockCapture(true); err != nil {
		return err
	}
	success := false
	_ = x.transport.SetDeadline(time.Now().Add(cursorIOTimeout))
	defer func() {
		if !success {
			x.unlockCapture()
		}
		_ = x.transport.SetDeadline(time.Time{})
	}()
	painted, err := x.painted()
	if err != nil {
		return err
	}
	x.restore = nil
	// Every draw and remap records paint history before making pixels visible.
	// A desktop without visible or retired cues needs no window scan or fence.
	if !painted {
		success = true
		return nil
	}
	if err = shape.Init(x.conn); err != nil {
		return err
	}
	if err = x.withServerGrab(x.hideCaptureWindows); err != nil {
		return err
	}
	if err = x.checkCompositor(); err != nil {
		return err
	}
	if err = x.frameFence(); err != nil {
		return err
	}
	if err = x.setPainted(false); err != nil {
		return err
	}
	success = true
	return nil
}

func (x *cursorX11) hideCaptureWindows() error {
	windows, err := x.ownedWindows()
	if err != nil {
		return err
	}
	for window, restore := range windows {
		attributes, err := xproto.GetWindowAttributes(x.conn, window).Reply()
		if err != nil {
			return err
		}
		if attributes.MapState == xproto.MapStateUnmapped {
			continue
		}
		if restore {
			bounds, err := shape.GetRectangles(x.conn, window, shape.SkBounding).Reply()
			if err != nil {
				return err
			}
			if len(bounds.Rectangles) == 0 {
				continue
			}
			// Notify the renderer before mutating its window. If this client
			// disappears between layers, the renderer can repaint the whole cue.
			if err = xproto.ChangePropertyChecked(x.conn, xproto.PropModeReplace, window, x.hidden, xproto.AtomCardinal, 32, 5, x.leaseToken).Check(); err != nil {
				return err
			}
			x.restore = append(x.restore, cursorHiddenWindow{window, bounds.Rectangles})
		}
		if err = x.setWindowShape(window, nil).Check(); err != nil {
			return err
		}
	}
	return nil
}

func (x *cursorX11) setWindowShape(window xproto.Window, bounds []xproto.Rectangle) shape.RectanglesCookie {
	return shape.RectanglesChecked(x.conn, shape.SoSet, shape.SkBounding, xproto.ClipOrderingUnsorted, window, 0, 0, bounds)
}

func (x *cursorX11) endCapture() error {
	if !x.locked {
		return fmt.Errorf("capture not held")
	}
	_ = x.transport.SetDeadline(time.Now().Add(cursorIOTimeout))
	defer func() { x.unlockCapture(); _ = x.transport.SetDeadline(time.Time{}) }()
	if len(x.restore) == 0 {
		return nil
	}
	return x.withServerGrab(x.restoreCaptureWindows)
}

func (x *cursorX11) restoreCaptureWindows() error {
	painted := false
	for _, hidden := range x.restore {
		// The token is on the window itself. A new window with the same XID
		// has no claim to this capture's saved shape.
		property, err := xproto.GetProperty(x.conn, false, hidden.window, x.hidden, xproto.AtomCardinal, 0, 5).Reply()
		if _, gone := err.(xproto.WindowError); gone {
			continue
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(property.Value, x.leaseToken) {
			continue
		}
		if !painted {
			if err = x.setPainted(true); err != nil {
				return err
			}
			painted = true
		}
		if err = x.setWindowShape(hidden.window, hidden.shape).Check(); err != nil {
			return err
		}
		if err = xproto.DeletePropertyChecked(x.conn, hidden.window, x.hidden).Check(); err != nil {
			return err
		}
	}
	x.restore = nil
	return nil
}

// A shape change clips a mapped window without starting its unmap animation.
// The witness is shown after all overlay clips and then clipped itself. Reading
// both states fences compositor progress without relying on the background
// sampled before the compositor processed the overlay changes.
func (x *cursorX11) frameFence() error {
	if xproto.Setup(x.conn).ImageByteOrder != xproto.ImageOrderLSBFirst {
		return fmt.Errorf("unsupported capture witness pixel order")
	}
	geometry, err := xproto.GetGeometry(x.conn, xproto.Drawable(x.root)).Reply()
	if err != nil {
		return err
	}
	if geometry.Width == 0 || geometry.Height == 0 || geometry.Width > 32767 || geometry.Height > 32767 || x.screen.RootDepth != 24 {
		return fmt.Errorf("unsupported capture witness geometry")
	}
	probe, err := xproto.NewWindowId(x.conn)
	if err != nil {
		return err
	}
	var nonce [3]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	pixel := uint32(nonce[0])<<16 | uint32(nonce[1])<<8 | uint32(nonce[2])
	px, py := int16(geometry.Width-1), int16(geometry.Height-1)
	current, err := xproto.GetImage(x.conn, xproto.ImageFormatZPixmap, xproto.Drawable(x.root), px, py, 1, 1, 0xffffffff).Reply()
	if err != nil {
		return err
	}
	if len(current.Data) < 4 {
		return fmt.Errorf("capture witness unreadable")
	}
	if pixel == binary.LittleEndian.Uint32(current.Data[:4])&0xffffff {
		pixel ^= 0xffffff
	}
	if err = xproto.CreateWindowChecked(x.conn, 24, probe, x.root, px, py, 1, 1, 0, xproto.WindowClassInputOutput, x.screen.RootVisual, xproto.CwBackPixel|xproto.CwOverrideRedirect, []uint32{pixel, 1}).Check(); err != nil {
		return err
	}
	defer func() {
		shape.Rectangles(x.conn, shape.SoSet, shape.SkBounding, xproto.ClipOrderingUnsorted, probe, 0, 0, nil)
		xproto.DestroyWindow(x.conn, probe)
	}()
	if err = xproto.ChangePropertyChecked(x.conn, xproto.PropModeReplace, probe, x.marker, xproto.AtomCardinal, 32, 1, []byte{2, 0, 0, 0}).Check(); err != nil {
		return err
	}
	if err = shape.RectanglesChecked(x.conn, shape.SoSet, shape.SkInput, xproto.ClipOrderingUnsorted, probe, 0, 0, nil).Check(); err != nil {
		return err
	}
	if err = xproto.MapWindowChecked(x.conn, probe).Check(); err != nil {
		return err
	}
	if err = xproto.ConfigureWindowChecked(x.conn, probe, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove}).Check(); err != nil {
		return err
	}
	deadline := time.Now().Add(650 * time.Millisecond)
	if err = x.waitWitness(px, py, pixel, true, deadline); err != nil {
		return err
	}
	if err = x.setWindowShape(probe, nil).Check(); err != nil {
		return err
	}
	return x.waitWitness(px, py, pixel, false, deadline)
}

func (x *cursorX11) waitWitness(px, py int16, pixel uint32, visible bool, deadline time.Time) error {
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("compositor did not acknowledge cursor exclusion")
		}
		image, err := xproto.GetImage(x.conn, xproto.ImageFormatZPixmap, xproto.Drawable(x.root), px, py, 1, 1, 0xffffffff).Reply()
		if err != nil {
			return err
		}
		if len(image.Data) < 4 {
			return fmt.Errorf("capture witness unreadable")
		}
		if matches := binary.LittleEndian.Uint32(image.Data[:4])&0xffffff == pixel; matches == visible {
			return nil
		}
		select {
		case <-x.stop:
			return fmt.Errorf("cursor closed")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

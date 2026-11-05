//go:build linux

package provider

import (
	"fmt"
	"image"
	"math"
	"net"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
)

type cursorX11 struct {
	conn                           *xgb.Conn
	window                         xproto.Window
	edges                          [4]xproto.Window
	root                           xproto.Window
	gc                             xproto.Gcontext
	staging                        xproto.Pixmap
	stagingWidth, stagingHeight    uint16
	visual                         xproto.Visualid
	colormap                       xproto.Colormap
	compositor, captureSession     xproto.Atom
	mapped                         bool // Last presentation had visible layers. Capture can temporarily hide them.
	repaint                        bool
	transport                      net.Conn
	screen                         *xproto.ScreenInfo
	locked                         bool
	marker, history, owner, hidden xproto.Atom
	lockWindow                     xproto.Window
	leaseToken                     []byte
	restore                        []cursorHiddenWindow
	stop                           <-chan struct{}
}

func (x *cursorX11) initializeOverlay() (err error) {
	conn := x.conn
	if err = shape.Init(conn); err != nil {
		return err
	}
	version, err := shape.QueryVersion(conn).Reply()
	if err != nil || version.MajorVersion < 1 || version.MajorVersion == 1 && version.MinorVersion < 1 {
		return fmt.Errorf("X11 Shape input passthrough unavailable")
	}
	screen := x.screen
	if screen.RootDepth != 24 {
		return fmt.Errorf("cursor overlay requires a 24-bit X11 desktop")
	}
	x.visual = cursorARGBVisual(xproto.Setup(conn), screen)
	if x.visual == 0 {
		return fmt.Errorf("cursor overlay requires a little-endian ARGB32 X11 visual")
	}
	if err = x.checkCompositor(); err != nil {
		return err
	}
	if x.colormap, err = xproto.NewColormapId(conn); err != nil {
		return err
	}
	if err = xproto.CreateColormapChecked(conn, xproto.ColormapAllocNone, x.colormap, x.root, x.visual).Check(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if x.window != 0 {
				xproto.DestroyWindow(conn, x.window)
				x.window = 0
			}
			xproto.FreeColormap(conn, x.colormap)
		}
	}()
	if x.window, err = x.createOverlayWindow("HCU Agent Cursor"); err != nil {
		return err
	}
	if x.gc, err = xproto.NewGcontextId(conn); err != nil {
		return err
	}
	if err = xproto.CreateGCChecked(conn, x.gc, xproto.Drawable(x.window), xproto.GcGraphicsExposures, []uint32{0}).Check(); err != nil {
		return err
	}
	// Prepare all static poses before accepting events, so the first cue
	// does not pay the vector rasterization cost on its animation tick.
	cursorGlyph(false, false)
	return nil
}

func (x *cursorX11) createOverlayWindow(name string) (xproto.Window, error) {
	window, err := xproto.NewWindowId(x.conn)
	if err != nil {
		return 0, err
	}
	if err := xproto.CreateWindowChecked(x.conn, 32, window, x.root, 0, 0, 1, 1, 0, xproto.WindowClassInputOutput, x.visual, xproto.CwBackPixel|xproto.CwBorderPixel|xproto.CwOverrideRedirect|xproto.CwEventMask|xproto.CwColormap, []uint32{0, 0, 1, xproto.EventMaskPropertyChange, uint32(x.colormap)}).Check(); err != nil {
		return 0, err
	}
	input := shape.RectanglesChecked(x.conn, shape.SoSet, shape.SkInput, xproto.ClipOrderingUnsorted, window, 0, 0, nil)
	marker := xproto.ChangePropertyChecked(x.conn, xproto.PropModeReplace, window, x.marker, xproto.AtomCardinal, 32, 1, []byte{1, 0, 0, 0})
	title := xproto.ChangePropertyChecked(x.conn, xproto.PropModeReplace, window, xproto.AtomWmName, xproto.AtomString, 8, uint32(len(name)), []byte(name))
	for _, request := range []*xgb.Cookie{input.Cookie, marker.Cookie, title.Cookie, x.setWindowShape(window, nil).Cookie} {
		if err := request.Check(); err != nil {
			xproto.DestroyWindow(x.conn, window)
			return 0, err
		}
	}
	return window, nil
}

func (x *cursorX11) checkCompositor() error {
	owner, err := xproto.GetSelectionOwner(x.conn, x.compositor).Reply()
	if err != nil {
		return err
	}
	if owner.Owner == 0 {
		return fmt.Errorf("cursor overlay requires an X11 compositing manager for clean captures")
	}
	return nil
}

// Only this renderer's windows receive these notices. A capture client
// marks each layer before hiding it, so an interrupted group is recoverable.
func (x *cursorX11) pollCaptureChanges() error {
	for {
		event, err := x.conn.PollForEvent()
		if err != nil {
			return err
		}
		if event == nil {
			return nil
		}
		if property, ok := event.(xproto.PropertyNotifyEvent); ok && property.Atom == x.hidden && property.State == xproto.PropertyNewValue {
			x.repaint = true
		}
	}
}

func (x *cursorX11) hide() error {
	if !x.mapped {
		x.repaint = false
		return nil
	}
	if err := x.withServerGrab(func() error {
		requests := []*xgb.Cookie{x.setWindowShape(x.window, nil).Cookie}
		for _, window := range x.edges {
			if window != 0 {
				requests = append(requests, x.setWindowShape(window, nil).Cookie)
			}
		}
		return x.checkFrameRequests(requests)
	}); err != nil {
		return err
	}
	x.mapped = false
	x.repaint = false
	return nil
}

func (x *cursorX11) draw(visual cursorVisual, now time.Time) error {
	if x.window == 0 {
		return nil
	}
	if err := x.pollCaptureChanges(); err != nil {
		return err
	}
	acquired, err := x.lockCapture(false)
	if err != nil || !acquired {
		return err
	}
	_ = x.transport.SetDeadline(time.Now().Add(cursorIOTimeout))
	defer func() { x.unlockCapture(); _ = x.transport.SetDeadline(time.Time{}) }()
	if !visual.placed || now.Sub(visual.last) >= cursorIdleTime {
		return x.hide()
	}
	if err := x.checkCompositor(); err != nil {
		return err
	}
	geometry, err := xproto.GetGeometry(x.conn, xproto.Drawable(x.root)).Reply()
	if err != nil {
		return err
	}
	width, height := int(geometry.Width), int(geometry.Height)
	if width > 32767 || height > 32767 {
		return fmt.Errorf("cursor display exceeds X11 drawing coordinates")
	}
	paint := cursorPixels(visual, now, width, height)
	if paint.bounds.Empty() && len(paint.highlights) == 0 {
		return x.hide()
	}
	for i := range paint.highlights {
		if x.edges[i] == 0 {
			x.edges[i], err = x.createOverlayWindow("HCU Agent Target Edge")
			if err != nil {
				return err
			}
		}
	}
	if err = x.setPainted(true); err != nil {
		return err
	}
	if err := x.stageCursor(paint); err != nil {
		return err
	}
	if err := x.withServerGrab(func() error { return x.present(visual, now, paint) }); err != nil {
		return err
	}
	if err := x.pollCaptureChanges(); err != nil {
		return err
	}
	x.repaint = false
	x.mapped = true
	return nil
}

// Stage the tile without moving the visible window or grabbing the server.
func (x *cursorX11) stageCursor(paint cursorPaint) error {
	if paint.bounds.Empty() {
		return nil
	}
	width, height := uint16(paint.bounds.Dx()), uint16(paint.bounds.Dy())
	if width > x.stagingWidth || height > x.stagingHeight {
		pixmap, err := xproto.NewPixmapId(x.conn)
		if err != nil {
			return err
		}
		stagingWidth, stagingHeight := max(width, x.stagingWidth), max(height, x.stagingHeight)
		if err = xproto.CreatePixmapChecked(x.conn, 32, pixmap, xproto.Drawable(x.window), stagingWidth, stagingHeight).Check(); err != nil {
			return err
		}
		if x.staging != 0 {
			xproto.FreePixmap(x.conn, x.staging)
		}
		x.staging = pixmap
		x.stagingWidth, x.stagingHeight = stagingWidth, stagingHeight
	}
	return xproto.PutImageChecked(x.conn, xproto.ImageFormatZPixmap, xproto.Drawable(x.staging), x.gc, width, height, 0, 0, 0, 32, paint.pixels).Check()
}

func (x *cursorX11) present(visual cursorVisual, now time.Time, paint cursorPaint) error {
	var requests []*xgb.Cookie
	if paint.bounds.Empty() {
		requests = append(requests, x.setWindowShape(x.window, nil).Cookie)
	} else {
		rect := cursorXRectangle(paint.bounds)
		requests = append(requests, x.configureOverlayWindow(x.window, rect),
			shape.MaskChecked(x.conn, shape.SoSet, shape.SkBounding, x.window, 0, 0, 0).Cookie,
			xproto.MapWindowChecked(x.conn, x.window).Cookie,
			xproto.CopyAreaChecked(x.conn, xproto.Drawable(x.staging), xproto.Drawable(x.window), x.gc, 0, 0, 0, 0, rect.Width, rect.Height).Cookie)
	}
	if len(paint.highlights) > 0 {
		accent := cursorAccent(visual.failed)
		alpha := uint32(math.Round(112 * clampCursor(float64(cursorIdleTime-now.Sub(visual.last))/float64(300*time.Millisecond))))
		ink := alpha<<24 | (uint32(accent.R)*alpha/255)<<16 | (uint32(accent.G)*alpha/255)<<8 | uint32(accent.B)*alpha/255
		requests = append(requests, xproto.ChangeGCChecked(x.conn, x.gc, xproto.GcForeground, []uint32{ink}).Cookie)
	}
	for i, window := range x.edges {
		if window == 0 {
			continue
		}
		if i >= len(paint.highlights) {
			requests = append(requests, x.setWindowShape(window, nil).Cookie)
			continue
		}
		rect := paint.highlights[i]
		requests = append(requests, x.configureOverlayWindow(window, rect),
			shape.MaskChecked(x.conn, shape.SoSet, shape.SkBounding, window, 0, 0, 0).Cookie,
			xproto.MapWindowChecked(x.conn, window).Cookie,
			xproto.PolyFillRectangleChecked(x.conn, xproto.Drawable(window), x.gc, []xproto.Rectangle{{Width: rect.Width, Height: rect.Height}}).Cookie)
	}
	return x.checkFrameRequests(requests)
}

func (x *cursorX11) checkFrameRequests(requests []*xgb.Cookie) error {
	// Check the entire frame after a single round trip.
	x.conn.Sync()
	for _, request := range requests {
		if err := request.Check(); err != nil {
			return err
		}
	}
	return nil
}

func (x *cursorX11) configureOverlayWindow(window xproto.Window, rect xproto.Rectangle) *xgb.Cookie {
	return xproto.ConfigureWindowChecked(x.conn, window,
		xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight|xproto.ConfigWindowStackMode,
		[]uint32{uint32(rect.X), uint32(rect.Y), uint32(rect.Width), uint32(rect.Height), xproto.StackModeAbove}).Cookie
}

func cursorXRectangle(rect image.Rectangle) xproto.Rectangle {
	return xproto.Rectangle{X: int16(rect.Min.X), Y: int16(rect.Min.Y), Width: uint16(rect.Dx()), Height: uint16(rect.Dy())}
}

func cursorARGBVisual(setup *xproto.SetupInfo, screen *xproto.ScreenInfo) xproto.Visualid {
	if setup.ImageByteOrder != xproto.ImageOrderLSBFirst {
		return 0
	}
	formatOK := false
	for _, format := range setup.PixmapFormats {
		if format.Depth == 32 && format.BitsPerPixel == 32 && format.ScanlinePad == 32 {
			formatOK = true
		}
	}
	if !formatOK {
		return 0
	}
	for _, depth := range screen.AllowedDepths {
		if depth.Depth != 32 {
			continue
		}
		for _, visual := range depth.Visuals {
			if visual.Class == xproto.VisualClassTrueColor && visual.RedMask == 0xff0000 && visual.GreenMask == 0xff00 && visual.BlueMask == 0xff {
				return visual.VisualId
			}
		}
	}
	return 0
}

func (x *cursorX11) retire() {
	if x.staging == 0 && x.edges[0] == 0 {
		return
	}
	// Give the compositor a chance to forget the visible shapes before
	// destroying the windows. Shutdown can still close the raw transport
	// immediately if X11 does not respond.
	x.stop = nil
	_ = x.transport.SetDeadline(time.Now().Add(cursorIOTimeout))
	if painted, err := x.painted(); err != nil || !painted {
		return
	}
	if !x.locked {
		if acquired, err := x.lockCapture(true); err != nil || !acquired {
			return
		}
	}
	_ = x.transport.SetDeadline(time.Now().Add(cursorIOTimeout))
	defer x.unlockCapture()
	if x.hide() == nil {
		_ = x.frameFence()
	}
}

func (x *cursorX11) close() {
	if x.transport != nil {
		_ = x.transport.Close()
	}
	if x.conn != nil {
		x.conn.Close()
	}
}

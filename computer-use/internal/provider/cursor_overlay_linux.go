//go:build linux

package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// cursorEvent is an operator-only side channel. It carries no text, keys,
// accessibility labels or model-visible results.
type cursorEvent struct {
	SchemaVersion int       `json:"schema_version"`
	Kind          string    `json:"kind"`
	Sequence      uint64    `json:"sequence,omitempty"`
	X             float64   `json:"x,omitempty"`
	Y             float64   `json:"y,omitempty"`
	Rect          []float64 `json:"rect,omitempty"`
}

func (e cursorEvent) validate() error {
	if e.SchemaVersion != 1 {
		return fmt.Errorf("unsupported cursor event schema")
	}
	switch e.Kind {
	case "move", "press", "release", "target", "end", "error":
	case "capture_begin", "capture_end", "capture_identity":
		if e.Sequence == 0 {
			return fmt.Errorf("capture sequence is missing")
		}
	default:
		return fmt.Errorf("unsupported cursor event")
	}
	for _, n := range append([]float64{e.X, e.Y}, e.Rect...) {
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1<<24 {
			return fmt.Errorf("invalid cursor geometry")
		}
	}
	if len(e.Rect) != 0 && (len(e.Rect) != 4 || e.Rect[2] <= 0 || e.Rect[3] <= 0) {
		return fmt.Errorf("invalid cursor rectangle")
	}
	return nil
}

type cursorReply struct {
	session string
	err     error
}

type cursorCommand struct {
	event cursorEvent
	done  chan cursorReply
}

type linuxCursorOverlay struct {
	socket      net.Conn
	commands    chan cursorCommand
	stop        chan struct{}
	stopOnce    sync.Once
	once        sync.Once
	mu          sync.Mutex
	transport   net.Conn
	environment []string
	enabled     bool
	renderDone  chan struct{}
	readerDone  chan struct{}
}

func cursorEnvironment(environment []string, key string) string {
	for i := len(environment) - 1; i >= 0; i-- {
		if strings.HasPrefix(environment[i], key+"=") {
			return strings.TrimPrefix(environment[i], key+"=")
		}
	}
	return ""
}

func startLinuxCursorOverlay(environment []string) (*linuxCursorOverlay, *os.File, error) {
	display := cursorEnvironment(environment, "DISPLAY")
	if display == "" || cursorEnvironment(environment, "WAYLAND_DISPLAY") != "" {
		return nil, nil, nil
	}
	value := strings.ToLower(strings.TrimSpace(cursorEnvironment(environment, "COMPUTER_USE_CURSOR")))
	enabled := value == "1" || value == "true" || value == "on"
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	parent := os.NewFile(uintptr(fds[0]), "cursor-parent")
	child := os.NewFile(uintptr(fds[1]), "cursor-provider")
	connection, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	c := &linuxCursorOverlay{socket: connection, commands: make(chan cursorCommand, 64), stop: make(chan struct{}), environment: append([]string{}, environment...), enabled: enabled, renderDone: make(chan struct{}), readerDone: make(chan struct{})}
	go c.run()
	go c.readEvents()
	return c, child, nil
}

func (c *linuxCursorOverlay) requestStop() {
	c.stopOnce.Do(func() {
		close(c.stop)
		_ = c.socket.Close()
	})
}

func (c *linuxCursorOverlay) close() {
	if c == nil {
		return
	}
	c.requestStop()
	c.once.Do(func() {
		c.mu.Lock()
		if c.transport != nil {
			_ = c.transport.Close()
		}
		c.mu.Unlock()
	})
}

func (c *linuxCursorOverlay) readEvents() {
	defer close(c.readerDone)
	defer c.requestStop()
	scanner := bufio.NewScanner(c.socket)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	for scanner.Scan() {
		var event cursorEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.validate() != nil {
			return
		}
		capture := event.Kind == "capture_begin" || event.Kind == "capture_end" || event.Kind == "capture_identity"
		if !c.enabled && !capture {
			continue
		}
		command := cursorCommand{event: event}
		if capture {
			command.done = make(chan cursorReply, 1)
		}
		if event.Kind == "move" {
			// Display can skip intermediate poses; it must never stall input.
			select {
			case c.commands <- command:
			default:
			}
			continue
		}
		select {
		case c.commands <- command:
		case <-c.stop:
			return
		}
		if !capture {
			continue
		}
		select {
		case result := <-command.done:
			_ = c.socket.SetWriteDeadline(time.Now().Add(time.Second))
			ack := struct {
				SchemaVersion int    `json:"schema_version"`
				Sequence      uint64 `json:"sequence"`
				OK            bool   `json:"ok"`
				Session       string `json:"session,omitempty"`
			}{1, event.Sequence, result.err == nil, result.session}
			if json.NewEncoder(c.socket).Encode(ack) != nil || result.err != nil {
				return
			}
		case <-c.stop:
			return
		}
	}
}

func (c *linuxCursorOverlay) trackConnection(connection net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transport = connection
	select {
	case <-c.stop:
		_ = connection.Close()
	default:
	}
}

func (c *linuxCursorOverlay) shutdown() {
	if c == nil {
		return
	}
	c.requestStop()
	timer := time.NewTimer(cursorIOTimeout + 250*time.Millisecond)
	defer timer.Stop()
	for _, done := range []chan struct{}{c.renderDone, c.readerDone} {
		select {
		case <-done:
		case <-timer.C:
			c.close()
			return
		}
	}
}

func (c *linuxCursorOverlay) run() {
	defer close(c.renderDone)
	defer c.close()
	var x *cursorX11
	defer func() {
		if x != nil {
			x.retire()
			x.close()
		}
	}()
	var ticker *time.Ticker
	var ticks <-chan time.Time
	setTicking := func(active bool) {
		if active && ticks == nil {
			if ticker == nil {
				ticker = time.NewTicker(time.Second / 60)
			} else {
				ticker.Reset(time.Second / 60)
			}
			ticks = ticker.C
		} else if !active && ticks != nil {
			ticker.Stop()
			ticks = nil
		}
	}
	defer setTicking(false)
	var visual cursorVisual
	var captureSequence uint64
	visualEnabled := c.enabled
	for {
		select {
		case <-c.stop:
			return
		case command := <-c.commands:
			event := command.event
			var err error
			var session string
			if x == nil {
				x, err = openCursorX11(c.environment, c.trackConnection)
				if err == nil {
					x.stop = c.stop
					if c.enabled {
						_ = x.transport.SetDeadline(time.Now().Add(cursorIOTimeout))
						if visualErr := x.initializeOverlay(); visualErr != nil {
							writeLinuxProviderProcessDiagnostic("cursor drawing disabled: " + visualErr.Error())
							visualEnabled = false
						}
						_ = x.transport.SetDeadline(time.Time{})
					}
				}
			}
			if err == nil {
				switch event.Kind {
				case "capture_identity":
					session, err = x.captureIdentity()
				case "capture_begin":
					if captureSequence != 0 {
						err = fmt.Errorf("capture already held")
					} else {
						err = x.beginCapture()
						if err == nil {
							captureSequence = event.Sequence
						}
					}
				case "capture_end":
					if captureSequence != event.Sequence {
						err = fmt.Errorf("capture sequence mismatch")
					} else {
						err = x.endCapture()
						captureSequence = 0
					}
				default:
					if !visualEnabled {
						continue
					}
					visual.apply(event, time.Now())
					if captureSequence == 0 {
						err = x.draw(visual, time.Now())
					}
				}
			}
			if command.done != nil {
				command.done <- cursorReply{session, err}
			}
			if err != nil {
				writeLinuxProviderProcessDiagnostic("cursor overlay stopped: " + err.Error())
				if command.done != nil {
					select {
					case <-c.readerDone:
					case <-c.stop:
					}
				}
				return
			}
			setTicking(visualEnabled && visual.placed && captureSequence == 0 && (x.mapped || time.Since(visual.last) < cursorIdleTime))
		case now := <-ticks:
			if err := x.pollCaptureChanges(); err != nil {
				writeLinuxProviderProcessDiagnostic("cursor overlay stopped: " + err.Error())
				return
			}
			if now.Sub(visual.last) >= cursorIdleTime {
				if x.mapped {
					if err := x.draw(visual, now); err != nil {
						return
					}
				}
				if !x.mapped {
					setTicking(false)
				}
				continue
			}
			if x.repaint || now.Sub(visual.started) < cursorMoveTime+time.Second/60 || now.Sub(visual.pulse) < cursorPulseTime || now.Sub(visual.last) > cursorIdleTime-300*time.Millisecond {
				if err := x.draw(visual, now); err != nil {
					writeLinuxProviderProcessDiagnostic("cursor overlay stopped: " + err.Error())
					return
				}
			}
		}
	}
}

//go:build linux

package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
)

type cursorMCPClient struct {
	cmd       *exec.Cmd
	input     io.WriteCloser
	output    *bufio.Reader
	sequence  int
	lastImage string
}

func newCursorMCPClient(t *testing.T, enabled bool) *cursorMCPClient {
	t.Helper()
	requireCursorDesktop(t)
	binary := os.Getenv("COMPUTER_USE_CURSOR_TEST_BINARY")
	if binary == "" {
		t.Skip("set COMPUTER_USE_CURSOR_TEST_BINARY to a built Linux binary")
	}
	client := &cursorMCPClient{cmd: exec.Command(binary, "mcp")}
	flag := "0"
	if enabled {
		flag = "1"
	}
	client.cmd.Env = append(os.Environ(), "COMPUTER_USE_CURSOR="+flag)
	log, err := os.Create(filepath.Join(t.TempDir(), "mcp.log"))
	if err != nil {
		t.Fatal(err)
	}
	client.cmd.Stderr = log
	client.input, err = client.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := client.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	client.output = bufio.NewReader(output)
	if err = client.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.input.Close()
		done := make(chan struct{})
		go func() { _ = client.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = client.cmd.Process.Kill()
			<-done
		}
		log.Close()
	})
	client.request(t, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cursor-desktop-test", "version": "1"}})
	return client
}

func (client *cursorMCPClient) request(t *testing.T, method string, params map[string]any) map[string]any {
	t.Helper()
	client.sequence++
	if err := json.NewEncoder(client.input).Encode(map[string]any{"jsonrpc": "2.0", "id": client.sequence, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
	type response struct {
		value map[string]any
		err   error
	}
	done := make(chan response, 1)
	go func() {
		for {
			line, err := client.output.ReadBytes('\n')
			if err != nil {
				done <- response{err: err}
				return
			}
			var value map[string]any
			if err = json.Unmarshal(line, &value); err != nil {
				done <- response{err: err}
				return
			}
			if value["id"] == float64(client.sequence) {
				done <- response{value: value}
				return
			}
		}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.value["error"] != nil {
			t.Fatalf("MCP error: %v", result.value["error"])
		}
		value, ok := result.value["result"].(map[string]any)
		if !ok {
			t.Fatal("MCP result missing")
		}
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("MCP request did not finish")
		return nil
	}
}

func (client *cursorMCPClient) gui(t *testing.T, actions ...map[string]any) (bool, map[string]any) {
	t.Helper()
	result := client.request(t, "tools/call", map[string]any{"name": "GUI", "arguments": map[string]any{"actions": actions}})
	var status map[string]any
	content, _ := result["content"].([]any)
	for _, item := range content {
		block, _ := item.(map[string]any)
		if block["type"] == "text" && status == nil {
			text, _ := block["text"].(string)
			_ = json.Unmarshal([]byte(text), &status)
		}
		if block["type"] == "image" {
			client.lastImage, _ = block["data"].(string)
		}
	}
	return result["isError"] != true, status
}

func cursorTestActor(t *testing.T) (string, []float64) {
	t.Helper()
	requireCursorDesktop(t)
	dir := t.TempDir()
	counter := filepath.Join(dir, "counter")
	geometry := filepath.Join(dir, "geometry")
	code := `import gi, json, sys
gi.require_version("Gtk", "3.0")
from gi.repository import Gtk, GLib
from pathlib import Path
w=Gtk.Window(title="HCU cursor recovery fixture")
w.set_default_size(640,360)
w.move(120,120)
box=Gtk.Box(orientation=Gtk.Orientation.VERTICAL,spacing=24)
box.set_border_width(30)
w.add(box)
label=Gtk.Label(label="Cursor recovery")
box.pack_start(label,False,False,0)
button=Gtk.Button(label="Increment")
box.pack_start(button,False,False,0)
count=[0]
def clicked(_):
 count[0]+=1
 Path(sys.argv[1]).write_text(str(count[0]))
button.connect("clicked",clicked)
def report():
 origin=w.get_window().get_origin()
 point=button.translate_coordinates(w,0,0)
 Path(sys.argv[2]).write_text(json.dumps([origin[-2]+point[0]+button.get_allocated_width()/2,origin[-1]+point[1]+button.get_allocated_height()/2]))
 return False
w.show_all()
GLib.timeout_add(100,report)
Gtk.main()
`
	app := exec.Command("python3", "-c", code, counter, geometry)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Process.Kill(); _ = app.Wait() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(geometry); err == nil {
			var coordinates []float64
			if json.Unmarshal(data, &coordinates) == nil && len(coordinates) == 2 {
				return counter, coordinates
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("GTK test actor did not initialize")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func cursorMCPWindow(t *testing.T, x *cursorX11) xproto.Window {
	t.Helper()
	windows, err := x.ownedWindows()
	if err != nil {
		t.Fatal(err)
	}
	var found []xproto.Window
	for window, regular := range windows {
		if regular {
			found = append(found, window)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one test cursor, got %d", len(found))
	}
	return found[0]
}

func TestCursorDesktopMCPRecoversWithoutReplayingInput(t *testing.T) {
	counter, coordinates := cursorTestActor(t)
	client := newCursorMCPClient(t, true)
	ok, status := client.gui(t, map[string]any{"type": "screenshot"})
	if !ok {
		t.Fatalf("initial screenshot: %v", status)
	}
	observer, err := openCursorX11(os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	window := cursorMCPWindow(t, observer)
	if err = xproto.KillClientChecked(observer.conn, uint32(window)).Check(); err != nil {
		t.Fatal(err)
	}
	// The first read may fail closed, but later reads use a fresh worker.
	successes := 0
	for attempt := 0; attempt < 3; attempt++ {
		if ok, status = client.gui(t, map[string]any{"type": "screenshot"}); ok {
			successes++
		}
	}
	if successes < 2 {
		t.Fatalf("renderer loss permanently disabled screenshots: %v", status)
	}
	screen := status["screen"].(map[string]any)
	geometry, err := xproto.GetGeometry(observer.conn, xproto.Drawable(observer.root)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	x := coordinates[0] * screen["width"].(float64) / float64(geometry.Width)
	y := coordinates[1] * screen["height"].(float64) / float64(geometry.Height)
	window = cursorMCPWindow(t, observer)
	fault := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if data, _ := os.ReadFile(counter); string(data) == "1" {
				fault <- xproto.KillClientChecked(observer.conn, uint32(window)).Check()
				return
			}
			if time.Now().After(deadline) {
				fault <- fmt.Errorf("click was not delivered")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	ok, status = client.gui(t, map[string]any{"type": "click", "x": x, "y": y}, map[string]any{"type": "wait"})
	if err = <-fault; err != nil {
		t.Fatal(err)
	}
	if ok || status["input_emitted"] != true {
		t.Fatalf("lost partial input evidence: %v", status)
	}
	if ok, status = client.gui(t, map[string]any{"type": "screenshot"}); !ok {
		t.Fatalf("next screenshot failed: %v", status)
	}
	data, err := os.ReadFile(counter)
	if err != nil || string(data) != "1" {
		t.Fatalf("input was replayed during recovery: %q, %v", data, err)
	}
}

func TestCursorDesktopMCPReaderWithoutOverlay(t *testing.T) {
	_, _ = cursorTestActor(t)
	writer := newCursorMCPClient(t, true)
	if ok, status := writer.gui(t, map[string]any{"type": "screenshot"}); !ok {
		t.Fatalf("initial screenshot: %v", status)
	}
	if ok, status := writer.gui(t, map[string]any{"type": "move", "x": 700, "y": 450}); !ok {
		t.Fatalf("move: %v", status)
	}
	baseline := writer.lastImage
	reader := newCursorMCPClient(t, false)
	if ok, status := reader.gui(t, map[string]any{"type": "screenshot"}); !ok {
		t.Fatalf("independent reader failed: %v", status)
	}
	if baseline == "" || reader.lastImage != baseline {
		t.Fatal("independent MCP captured a cursor or changed the desktop image")
	}
}

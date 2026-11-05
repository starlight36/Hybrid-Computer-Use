//go:build linux

package provider

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCursorDisplayEndpoints(t *testing.T) {
	for _, test := range []struct {
		display, network, address string
		screen                    int
	}{
		{":99", "unix", "/tmp/.X11-unix/X99", 0},
		{"unix/:99.1", "unix", "/tmp/.X11-unix/X99", 1},
		{"tcp/127.0.0.1:9", "tcp", "127.0.0.1:6009", 0},
	} {
		endpoint, err := parseCursorDisplay(test.display)
		if err != nil || endpoint.network != test.network || endpoint.address != test.address || endpoint.screen != test.screen {
			t.Fatalf("%s: %+v, %v", test.display, endpoint, err)
		}
	}
	for _, invalid := range []string{"", ":-1", ":999999", ":0.-1", "garbage"} {
		if _, err := parseCursorDisplay(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestCursorAuthorityMatchesDisplay(t *testing.T) {
	var record bytes.Buffer
	_ = binary.Write(&record, binary.BigEndian, uint16(65535))
	cookie := []byte("synthetic-cookie")
	for _, field := range [][]byte{[]byte(""), []byte("99"), []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		_ = binary.Write(&record, binary.BigEndian, uint16(len(field)))
		_, _ = record.Write(field)
	}
	path := filepath.Join(t.TempDir(), "authority")
	if err := os.WriteFile(path, record.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for _, number := range []string{"99", "98"} {
		got, err := cursorCookie([]string{"XAUTHORITY=" + path}, cursorEndpoint{network: "unix", number: number})
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if number == "99" {
			want = hex.EncodeToString(cookie)
		}
		if got != want {
			t.Fatal("authority matched the wrong display")
		}
	}
}

func TestCursorHandshakeHasDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if port < 6000 {
		t.Skip("test port cannot represent an X11 display")
	}
	display := fmt.Sprintf("127.0.0.1:%d", port-6000)
	done := make(chan error, 1)
	go func() {
		x, err := newCursorX11(display)
		if x != nil {
			x.close()
		}
		done <- err
	}()
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("silent X11 peer accepted")
		}
	case <-time.After(2 * cursorIOTimeout):
		peer.Close()
		t.Fatal("X11 handshake has no bounded deadline")
	}
}

func TestCursorAuthorityRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := cursorCookie([]string{"XAUTHORITY=" + path}, cursorEndpoint{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted non-regular Xauthority")
		}
	case <-time.After(cursorIOTimeout):
		t.Fatal("opening Xauthority blocked waiting for a FIFO writer")
	}
}

//go:build linux

package provider

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

const cursorIOTimeout = time.Second

type cursorEndpoint struct {
	network, address, host, number string
	screen                         int
}

func parseCursorDisplay(display string) (cursorEndpoint, error) {
	var result cursorEndpoint
	colon := strings.LastIndex(display, ":")
	if colon < 0 {
		return result, fmt.Errorf("invalid X11 display")
	}
	left, right := display[:colon], display[colon+1:]
	if dot := strings.Index(right, "."); dot >= 0 {
		screen, err := strconv.Atoi(right[dot+1:])
		if err != nil || screen < 0 {
			return result, fmt.Errorf("invalid X11 screen")
		}
		result.screen = screen
		right = right[:dot]
	}
	number, err := strconv.Atoi(right)
	if err != nil || number < 0 || number > 59535 {
		return result, fmt.Errorf("invalid X11 display number")
	}
	result.number = strconv.Itoa(number)
	if strings.HasPrefix(left, "/") {
		result.network, result.address = "unix", left+":"+right
		return result, nil
	}
	left = strings.TrimPrefix(strings.TrimPrefix(left, "tcp/"), "unix/")
	if left == "" || left == "unix" {
		result.network, result.address = "unix", "/tmp/.X11-unix/X"+result.number
		return result, nil
	}
	result.host = strings.Trim(left, "[]")
	result.network, result.address = "tcp", net.JoinHostPort(result.host, strconv.Itoa(6000+number))
	return result, nil
}

// Parse bounded Xauthority records without changing process-global environment.
func cursorCookie(environment []string, endpoint cursorEndpoint) (string, error) {
	path := cursorEnvironment(environment, "XAUTHORITY")
	if path == "" {
		path = filepath.Join(cursorEnvironment(environment, "HOME"), ".Xauthority")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Xauthority must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		return "", err
	}
	r := bytes.NewReader(data)
	hostname, _ := os.Hostname()
	for r.Len() > 0 {
		var family uint16
		if err = binary.Read(r, binary.BigEndian, &family); err != nil {
			return "", fmt.Errorf("invalid Xauthority record")
		}
		fields := make([][]byte, 4)
		for i := range fields {
			var size uint16
			if binary.Read(r, binary.BigEndian, &size) != nil || int(size) > r.Len() {
				return "", fmt.Errorf("invalid Xauthority field")
			}
			fields[i] = make([]byte, int(size))
			_, _ = io.ReadFull(r, fields[i])
		}
		local := endpoint.network == "unix" || endpoint.host == "localhost" || net.ParseIP(endpoint.host).IsLoopback()
		addressOK := family == 65535 || family == 256 && local && string(fields[0]) == hostname
		if family == 0 {
			addressOK = bytes.Equal(net.ParseIP(endpoint.host).To4(), fields[0]) && len(fields[0]) == 4
		}
		if family == 6 {
			addressOK = bytes.Equal(net.ParseIP(endpoint.host).To16(), fields[0]) && len(fields[0]) == 16
		}
		if addressOK && (len(fields[1]) == 0 || string(fields[1]) == endpoint.number) {
			if string(fields[2]) != "MIT-MAGIC-COOKIE-1" || len(fields[3]) != 16 {
				return "", fmt.Errorf("unsupported Xauthority protocol")
			}
			return hex.EncodeToString(fields[3]), nil
		}
	}
	return "", nil
}

func openCursorX11(environment []string, track func(net.Conn)) (*cursorX11, error) {
	endpoint, err := parseCursorDisplay(cursorEnvironment(environment, "DISPLAY"))
	if err != nil {
		return nil, err
	}
	cookie, err := cursorCookie(environment, endpoint)
	if err != nil {
		return nil, err
	}
	raw, err := net.DialTimeout(endpoint.network, endpoint.address, cursorIOTimeout)
	if err != nil {
		return nil, err
	}
	if track != nil {
		track(raw)
	}
	_ = raw.SetDeadline(time.Now().Add(cursorIOTimeout))
	connection, err := xgb.NewConnNetWithCookieHex(raw, cookie)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	connection.DefaultScreen = endpoint.screen
	if endpoint.screen >= len(xproto.Setup(connection).Roots) {
		raw.Close()
		connection.Close()
		return nil, fmt.Errorf("X11 screen unavailable")
	}
	x := &cursorX11{conn: connection, transport: raw, screen: xproto.Setup(connection).DefaultScreen(connection)}
	x.root = x.screen.Root
	if err = x.initializeCapture(); err != nil {
		x.close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return x, nil
}

func newCursorX11(display string) (*cursorX11, error) {
	environment := append(os.Environ(), "DISPLAY="+display)
	x, err := openCursorX11(environment, nil)
	if err != nil {
		return nil, err
	}
	_ = x.transport.SetDeadline(time.Now().Add(cursorIOTimeout))
	if err = x.initializeOverlay(); err != nil {
		x.close()
		return nil, err
	}
	_ = x.transport.SetDeadline(time.Time{})
	return x, nil
}

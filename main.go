package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// --- evdev / input constants ---

const (
	EV_KEY = 0x01
	EV_ABS = 0x03

	BTN_SOUTH  = 0x130
	BTN_EAST   = 0x131
	BTN_NORTH  = 0x133
	BTN_WEST   = 0x134
	BTN_TL     = 0x136
	BTN_TR     = 0x137
	BTN_TL2    = 0x138
	BTN_TR2    = 0x139
	BTN_SELECT = 0x13a
	BTN_START  = 0x13b
	BTN_MODE   = 0x13c
	BTN_THUMBL = 0x13d
	BTN_THUMBR = 0x13e

	ABS_X     = 0x00
	ABS_Y     = 0x01
	ABS_RX    = 0x03
	ABS_RY    = 0x04
	ABS_HAT0X = 0x10
	ABS_HAT0Y = 0x11

	stickDeadzone = 15

	EVIOCGNAME = 0x81004506
)

type inputEvent struct {
	Time  syscall.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

// --- hidraw haptic engine ---

const hidReportSize = 78 // DualSense BT output report

type hapticEngine struct {
	hidraw  *os.File
	seq     byte
	pulseCh chan pulseRequest
	verbose bool
}

type pulseRequest struct {
	right, left byte
	durationMs  int
}

func newHapticEngine(hidrawPath string) (*hapticEngine, error) {
	f, err := os.OpenFile(hidrawPath, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	h := &hapticEngine{
		hidraw:  f,
		pulseCh: make(chan pulseRequest, 16),
	}
	go h.runPulseLoop()
	return h, nil
}

func (h *hapticEngine) Close() {
	close(h.pulseCh)
	h.hidraw.Close()
}

func (h *hapticEngine) buildReport(rightMotor, leftMotor byte) []byte {
	buf := make([]byte, hidReportSize)
	buf[0] = 0x31
	buf[1] = (h.seq << 4) | 0x02
	buf[2] = 0x03
	buf[3] = 0x15
	buf[4] = rightMotor
	buf[5] = leftMotor
	h.seq = (h.seq + 1) & 0x0F

	crcData := make([]byte, 1+hidReportSize-4)
	crcData[0] = 0xA2
	copy(crcData[1:], buf[:hidReportSize-4])
	binary.LittleEndian.PutUint32(buf[hidReportSize-4:], crc32.ChecksumIEEE(crcData))
	return buf
}

func (h *hapticEngine) setMotors(right, left byte) {
	h.hidraw.Write(h.buildReport(right, left))
}

func (h *hapticEngine) pulse(right, left byte, durationMs int) {
	h.pulseCh <- pulseRequest{right, left, durationMs}
}

// runPulseLoop processes pulse requests sequentially in its own goroutine.
// All setMotors calls happen here — fully synchronous.
func (h *hapticEngine) runPulseLoop() {
	for req := range h.pulseCh {
		for {
			select {
			case newer, ok := <-h.pulseCh:
				if !ok {
					h.setMotors(0, 0)
					return
				}
				req = newer
			default:
				goto drained
			}
		}
	drained:
		h.setMotors(req.right, req.left)
		time.Sleep(time.Duration(req.durationMs) * time.Millisecond)
		h.setMotors(0, 0)
		time.Sleep(3 * time.Millisecond)
	}
}

// --- haptic profile ---

type pulseParams struct {
	right    byte
	left     byte
	duration int
}

type hapticPair struct {
	press   pulseParams
	release pulseParams
}

const (
	VBTN_DPAD_LEFT  = 0x1000
	VBTN_DPAD_RIGHT = 0x1001
	VBTN_DPAD_UP    = 0x1002
	VBTN_DPAD_DOWN  = 0x1003
	VBTN_LS_LEFT    = 0x1010
	VBTN_LS_RIGHT   = 0x1011
	VBTN_LS_UP      = 0x1012
	VBTN_LS_DOWN    = 0x1013
	VBTN_RS_LEFT    = 0x1020
	VBTN_RS_RIGHT   = 0x1021
	VBTN_RS_UP      = 0x1022
	VBTN_RS_DOWN    = 0x1023
)

var inputNames = map[uint16]string{
	BTN_SOUTH: "Cross", BTN_EAST: "Circle", BTN_NORTH: "Triangle", BTN_WEST: "Square",
	BTN_TL: "L1", BTN_TR: "R1", BTN_TL2: "L2", BTN_TR2: "R2",
	BTN_SELECT: "Create", BTN_START: "Options", BTN_MODE: "PS",
	BTN_THUMBL: "L3", BTN_THUMBR: "R3",
	VBTN_DPAD_LEFT: "D-pad Left", VBTN_DPAD_RIGHT: "D-pad Right",
	VBTN_DPAD_UP: "D-pad Up", VBTN_DPAD_DOWN: "D-pad Down",
	VBTN_LS_LEFT: "LS Left", VBTN_LS_RIGHT: "LS Right",
	VBTN_LS_UP: "LS Up", VBTN_LS_DOWN: "LS Down",
	VBTN_RS_LEFT: "RS Left", VBTN_RS_RIGHT: "RS Right",
	VBTN_RS_UP: "RS Up", VBTN_RS_DOWN: "RS Down",
}

func defaultProfile() map[uint16]hapticPair {
	// Trackpad-style: high-freq dominant, low-freq minimal
	click := hapticPair{
		press:   pulseParams{right: 255, left: 10, duration: 15},
		release: pulseParams{right: 180, left: 0, duration: 15},
	}
	press := hapticPair{
		press:   pulseParams{right: 255, left: 20, duration: 18},
		release: pulseParams{right: 200, left: 0, duration: 15},
	}
	thud := hapticPair{
		press:   pulseParams{right: 255, left: 50, duration: 22},
		release: pulseParams{right: 200, left: 15, duration: 18},
	}
	tick := hapticPair{
		press:   pulseParams{right: 220, left: 0, duration: 15},
		release: pulseParams{right: 150, left: 0, duration: 15},
	}
	axis := hapticPair{
		press:   pulseParams{right: 255, left: 10, duration: 15},
		release: pulseParams{right: 180, left: 0, duration: 15},
	}

	return map[uint16]hapticPair{
		BTN_SOUTH: click, BTN_EAST: click, BTN_NORTH: click, BTN_WEST: click,
		BTN_TL: press, BTN_TR: press,
		BTN_TL2: thud, BTN_TR2: thud,
		BTN_THUMBL: tick, BTN_THUMBR: tick,
		BTN_SELECT: click, BTN_START: click, BTN_MODE: tick,

		VBTN_DPAD_LEFT: axis, VBTN_DPAD_RIGHT: axis,
		VBTN_DPAD_UP: axis, VBTN_DPAD_DOWN: axis,

		VBTN_LS_LEFT: axis, VBTN_LS_RIGHT: axis,
		VBTN_LS_UP: axis, VBTN_LS_DOWN: axis,
		VBTN_RS_LEFT: axis, VBTN_RS_RIGHT: axis,
		VBTN_RS_UP: axis, VBTN_RS_DOWN: axis,
	}
}

// --- axis edge detection ---

type stickState struct {
	mu     sync.Mutex
	active map[uint16]bool
}

func newStickState() *stickState {
	return &stickState{active: make(map[uint16]bool)}
}

// updateStick returns (vbtn, pressed). pressed=true means edge entered,
// pressed=false means edge released. edge=false means no transition.
func (s *stickState) updateStick(axisCode uint16, value int32) (vbtn uint16, pressed bool, edge bool) {
	var negBtn, posBtn uint16
	var center, deadzone int32

	switch axisCode {
	case ABS_HAT0X:
		negBtn, posBtn, center, deadzone = VBTN_DPAD_LEFT, VBTN_DPAD_RIGHT, 0, 0
	case ABS_HAT0Y:
		negBtn, posBtn, center, deadzone = VBTN_DPAD_UP, VBTN_DPAD_DOWN, 0, 0
	case ABS_X:
		negBtn, posBtn, center, deadzone = VBTN_LS_LEFT, VBTN_LS_RIGHT, 128, stickDeadzone
	case ABS_Y:
		negBtn, posBtn, center, deadzone = VBTN_LS_UP, VBTN_LS_DOWN, 128, stickDeadzone
	case ABS_RX:
		negBtn, posBtn, center, deadzone = VBTN_RS_LEFT, VBTN_RS_RIGHT, 128, stickDeadzone
	case ABS_RY:
		negBtn, posBtn, center, deadzone = VBTN_RS_UP, VBTN_RS_DOWN, 128, stickDeadzone
	default:
		return 0, false, false
	}

	diff := value - center
	nowNeg := diff < -deadzone
	nowPos := diff > deadzone

	s.mu.Lock()
	defer s.mu.Unlock()

	if nowNeg {
		if !s.active[negBtn] {
			s.active[negBtn] = true
			s.active[posBtn] = false
			return negBtn, true, true
		}
	} else if s.active[negBtn] {
		s.active[negBtn] = false
		return negBtn, false, true
	}
	if nowPos {
		if !s.active[posBtn] {
			s.active[posBtn] = true
			s.active[negBtn] = false
			return posBtn, true, true
		}
	} else if s.active[posBtn] {
		s.active[posBtn] = false
		return posBtn, false, true
	}
	return 0, false, false
}

// --- device discovery ---

func ioctl(fd uintptr, req uint, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func getDeviceName(fd uintptr) string {
	buf := make([]byte, 256)
	if ioctl(fd, EVIOCGNAME, unsafe.Pointer(&buf[0])) != nil {
		return "unknown"
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

func findDualSense() (evdev string, hidraw string, err error) {
	matches, _ := filepath.Glob("/dev/input/event*")
	for _, path := range matches {
		f, e := os.OpenFile(path, os.O_RDWR, 0)
		if e != nil {
			continue
		}
		name := getDeviceName(f.Fd())
		f.Close()
		if strings.Contains(name, "DualSense") && !strings.Contains(name, "Motion") && !strings.Contains(name, "Touchpad") {
			evdev = path
			break
		}
	}
	if evdev == "" {
		return "", "", fmt.Errorf("DualSense evdev not found")
	}

	hidrawMatches, _ := filepath.Glob("/sys/class/hidraw/hidraw*")
	for _, sysPath := range hidrawMatches {
		uevent, e := os.ReadFile(sysPath + "/device/uevent")
		if e != nil {
			continue
		}
		if strings.Contains(string(uevent), "DualSense") {
			hidraw = "/dev/" + filepath.Base(sysPath)
			break
		}
	}
	if hidraw == "" {
		return "", "", fmt.Errorf("DualSense hidraw not found")
	}
	return evdev, hidraw, nil
}

// --- main ---

func main() {
	verbose := flag.Bool("v", false, "verbose output")
	flag.Parse()

	evdevPath, hidrawPath, err := findDualSense()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hapcon:", err)
		os.Exit(1)
	}

	evf, err := os.OpenFile(evdevPath, os.O_RDONLY, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hapcon: open evdev %s: %v\n", evdevPath, err)
		os.Exit(1)
	}
	defer evf.Close()
	name := getDeviceName(evf.Fd())

	engine, err := newHapticEngine(hidrawPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hapcon: open hidraw %s: %v\n", hidrawPath, err)
		os.Exit(1)
	}
	defer engine.Close()

	prof := defaultProfile()

	fmt.Printf("hapcon: %s (%s) + hidraw (%s)\n", name, evdevPath, hidrawPath)
	fmt.Printf("  %d inputs mapped\n", len(prof))

	sticks := newStickState()
	evSize := int(unsafe.Sizeof(inputEvent{}))
	buf := make([]byte, evSize)

	for {
		_, err := io.ReadFull(evf, buf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hapcon: read: %v\n", err)
			break
		}

		typ := binary.LittleEndian.Uint16(buf[unsafe.Offsetof(inputEvent{}.Type):])
		code := binary.LittleEndian.Uint16(buf[unsafe.Offsetof(inputEvent{}.Code):])
		value := int32(binary.LittleEndian.Uint32(buf[unsafe.Offsetof(inputEvent{}.Value):]))

		var triggerCode uint16
		var pressed bool

		switch {
		case typ == EV_KEY && (value == 1 || value == 0):
			triggerCode = code
			pressed = value == 1
		case typ == EV_ABS:
			if vbtn, pr, edge := sticks.updateStick(code, value); edge {
				triggerCode = vbtn
				pressed = pr
			}
		}

		if triggerCode != 0 {
			if pair, ok := prof[triggerCode]; ok {
				p := pair.release
				action := "release"
				if pressed {
					p = pair.press
					action = "press"
				}
				engine.pulse(p.right, p.left, p.duration)
				if *verbose {
					n := inputNames[triggerCode]
					if n == "" {
						n = fmt.Sprintf("0x%x", triggerCode)
					}
					fmt.Printf("  -> %s [%s]\n", n, action)
				}
			}
		}
	}
}

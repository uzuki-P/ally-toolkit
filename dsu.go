package main

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
	"net"
	"strings"
	"sync"
	"time"
)

// A DSU (cemuhook) motion server: what Savage Wind, Cemu, Yuzu, Dolphin and
// other emulators read a controller's motion from. Protocol:
// https://v1993.github.io/cemuhook-protocol/. One controller, in slot 0: the
// Ally's own sensor. It listens on the loopback address only.

const (
	dsuPort        = 26760
	dsuProtocol    = 1001
	dsuVersionMsg  = 0x100000
	dsuPortsMsg    = 0x100001
	dsuDataMsg     = 0x100002
	dsuSubscribeOK = 5 * time.Second // a client asks for data again within this
)

var dsuMAC = [6]byte{0x41, 0x4c, 0x4c, 0x59, 0x00, 0x01} // ("ALLY", slot 0)

// dsuSample is one motion report in DSU's axes: acceleration in G, the turn
// in degrees a second (pitch, yaw, roll), its time in microseconds.
type dsuSample struct {
	accel, gyro [3]float32
	micros      uint64
}

type dsuServer struct {
	conn   *net.UDPConn
	server uint32
	mu     sync.Mutex
	subs   map[string]dsuSub
	seq    uint32
}

type dsuSub struct {
	addr *net.UDPAddr
	seen time.Time
}

func listenDSU(addr string) (*dsuServer, error) {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", ua)
	if err != nil {
		return nil, err
	}
	s := &dsuServer{conn: conn, server: uint32(time.Now().UnixNano()), subs: map[string]dsuSub{}}
	go s.serve()
	return s, nil
}

func (s *dsuServer) addr() *net.UDPAddr { return s.conn.LocalAddr().(*net.UDPAddr) }

func (s *dsuServer) close() { s.conn.Close() }

// clients is how many clients asked for motion lately.
func (s *dsuServer) clients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.subs {
		if time.Since(c.seen) < dsuSubscribeOK {
			n++
		}
	}
	return n
}

func (s *dsuServer) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue // (Windows: an ICMP "port unreachable" from a client that left)
		}
		s.handle(buf[:n], from)
	}
}

func (s *dsuServer) handle(p []byte, from *net.UDPAddr) {
	if len(p) < 20 || string(p[:4]) != "DSUC" || binary.LittleEndian.Uint16(p[4:]) != dsuProtocol {
		return
	}
	size := 16 + int(binary.LittleEndian.Uint16(p[6:]))
	if size < 20 || size > len(p) || binary.LittleEndian.Uint32(p[8:]) != dsuCRC(p[:size]) {
		return
	}
	p = p[:size]
	switch binary.LittleEndian.Uint32(p[16:]) {
	case dsuVersionMsg:
		out := s.header(dsuVersionMsg, 2)
		binary.LittleEndian.PutUint16(out[20:], dsuProtocol)
		s.send(out, from)
	case dsuPortsMsg:
		if len(p) < 24 {
			return
		}
		n := int(binary.LittleEndian.Uint32(p[20:]))
		for i := 0; i < n && i < 4 && 24+i < len(p); i++ {
			out := s.header(dsuPortsMsg, 12)
			s.slot(out, p[24+i])
			s.send(out, from)
		}
	case dsuDataMsg:
		// (registration: all slots, slot 0, or this MAC; there is only slot 0)
		if len(p) >= 28 && (p[20]&1 != 0 && p[21] != 0 || p[20]&2 != 0 && [6]byte(p[22:28]) != dsuMAC) {
			return
		}
		s.mu.Lock()
		s.subs[from.String()] = dsuSub{addr: from, seen: time.Now()}
		s.mu.Unlock()
	}
}

// header is a server packet of message type msg with n bytes after it.
func (s *dsuServer) header(msg uint32, n int) []byte {
	out := make([]byte, 20+n)
	copy(out, "DSUS")
	binary.LittleEndian.PutUint16(out[4:], dsuProtocol)
	binary.LittleEndian.PutUint16(out[6:], uint16(4+n))
	binary.LittleEndian.PutUint32(out[12:], s.server)
	binary.LittleEndian.PutUint32(out[16:], msg)
	return out
}

// slot writes the controller's description at out[20:31]: slot 0 is the
// Ally's sensor (connected, full gyro, USB, charged), the others empty.
func (s *dsuServer) slot(out []byte, slot byte) {
	out[20] = slot
	if slot != 0 {
		return
	}
	out[21], out[22], out[23] = 2, 2, 1
	copy(out[24:30], dsuMAC[:])
	out[30] = 0xef
}

func (s *dsuServer) send(out []byte, to *net.UDPAddr) {
	binary.LittleEndian.PutUint32(out[8:], dsuCRC(out))
	_, _ = s.conn.WriteToUDP(out, to)
}

// publish sends a motion report to every client subscribed lately.
func (s *dsuServer) publish(m dsuSample) {
	s.mu.Lock()
	var to []*net.UDPAddr
	for k, c := range s.subs {
		if time.Since(c.seen) >= dsuSubscribeOK {
			delete(s.subs, k)
			continue
		}
		to = append(to, c.addr)
	}
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	if len(to) == 0 {
		return
	}
	out := s.header(dsuDataMsg, 80)
	s.slot(out, 0)
	out[31] = 1 // connected
	binary.LittleEndian.PutUint32(out[32:], seq)
	for i := 40; i < 44; i++ {
		out[i] = 128 // sticks centred
	}
	binary.LittleEndian.PutUint64(out[68:], m.micros)
	for i := 0; i < 3; i++ {
		binary.LittleEndian.PutUint32(out[76+4*i:], math.Float32bits(m.accel[i]))
		binary.LittleEndian.PutUint32(out[88+4*i:], math.Float32bits(m.gyro[i]))
	}
	binary.LittleEndian.PutUint32(out[8:], dsuCRC(out))
	for _, a := range to {
		_, _ = s.conn.WriteToUDP(out, a)
	}
}

// dsuCRC is the packet's CRC-32 with its own CRC field taken as zero.
func dsuCRC(p []byte) uint32 {
	c := crc32.Update(0, crc32.IEEETable, p[:8])
	c = crc32.Update(c, crc32.IEEETable, []byte{0, 0, 0, 0})
	return crc32.Update(c, crc32.IEEETable, p[12:])
}

// ---- the Ally's axes to DSU's

// imuSigns are Handheld Companion's IMU matrix for a model: the signs of its X,
// Y and Z after its usual axis swap (X, Z, Y), in Resources/Devices/<model>.json.
type imuSigns [3]float64

type handheldModel struct {
	product     string // in the firmware's product name
	name        string
	gyro, accel imuSigns
}

// Handheld Companion's matrices for ASUS's handhelds (ROGAlly.json,
// ROGAllyX.json, XboxROGAlly.json, XboxROGAllyX.json), by the model numbers
// it finds them by; DefaultDevice.json's for anything else.
var handheldModels = []handheldModel{
	{"RC71L", "ROG Ally", imuSigns{-1, -1, 1}, imuSigns{-1, -1, 1}},
	{"RC72LA", "ROG Ally X", imuSigns{-1, -1, 1}, imuSigns{-1, -1, 1}},
	{"RC73YA", "ROG Xbox Ally", imuSigns{1, 1, -1}, imuSigns{-1, -1, 1}},
	{"RC73XA", "ROG Xbox Ally X", imuSigns{1, 1, -1}, imuSigns{-1, -1, 1}},
	{"RC74XA", "ROG Xbox Ally X", imuSigns{1, 1, -1}, imuSigns{-1, -1, 1}},
}

var defaultModel = handheldModel{"", "this PC", imuSigns{1, 1, 1}, imuSigns{1, 1, 1}}

func modelFor(product string) handheldModel {
	for _, m := range handheldModels {
		if containsFold(product, m.product) {
			return m
		}
	}
	return defaultModel
}

// toDSU turns a reading in Windows' axes into DSU's as Handheld Companion
// does for a handheld's own sensor: its model's matrix (the axis swap, then
// the signs), the yaw / roll swap it makes for a sensor in the handheld
// itself (Steering axis: Auto), its inversions, then its DSU packet's signs.
// Held as you play, the handheld reads as a controller lying flat.
func toDSU(m handheldModel, gyro, accel reading, invertUD, invertLR bool) (a, g [3]float32) {
	hc := func(s imuSigns, r reading) [3]float64 {
		v := [3]float64{s[0] * r.x, s[1] * r.z, s[2] * r.y} // (the matrix: X, Z, Y with its signs)
		return [3]float64{v[0], -v[2], -v[1]}               // (the yaw / roll swap)
	}
	gv, av := hc(m.gyro, gyro), hc(m.accel, accel)
	if invertLR {
		gv[1], gv[2], av[1], av[2] = -gv[1], -gv[2], -av[1], -av[2]
	}
	if invertUD {
		gv[0], gv[1], av[0], av[1] = -gv[0], -gv[1], -av[0], -av[1]
	}
	a = [3]float32{float32(-av[0]), float32(-av[1]), float32(-av[2])}
	g = [3]float32{float32(gv[0]), float32(-gv[1]), float32(-gv[2])}
	return a, g
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToUpper(s), strings.ToUpper(sub))
}

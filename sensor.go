package main

import (
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Ally's own motion sensor, as Windows gives it: Windows.Devices.Sensors'
// Gyrometer (degrees a second) and Accelerometer (G, gravity's direction).
// Their Windows Runtime interfaces are called through their vtables, so no
// cgo. Axes: X right, Y to the top of the screen, Z out of it.
//
// Windows only streams a sensor to someone subscribed to its ReadingChanged
// event (polling alone gets one stale reading), so each has an event handler:
// a COM delegate whose vtable is Go callbacks.

var (
	combase                    = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoUninitialize         = combase.NewProc("RoUninitialize")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")

	iidUnknown              = mustGUID("{00000000-0000-0000-c000-000000000046}")
	iidAgileObject          = mustGUID("{94ea2b94-e9cc-49e0-c0ff-ee64ca8f5b90}")
	iidGyrometerStatics     = mustGUID("{83b6e7c9-e49d-4b39-86e6-cd554be4c5c1}")
	iidAccelerometerStatics = mustGUID("{a5e28b74-5a87-4a2d-becc-0f906ea061dd}")
	// TypedEventHandler<Gyrometer, GyrometerReadingChangedEventArgs> and the
	// accelerometer's, as Windows names a parameterized interface.
	iidGyroHandler = pinterfaceIID("pinterface({9de1c534-6ae1-11e0-84e1-18a905bcc53f};" +
		"rc(Windows.Devices.Sensors.Gyrometer;{fdb9a9c4-84b1-4ca2-9763-9b589506c70c});" +
		"rc(Windows.Devices.Sensors.GyrometerReadingChangedEventArgs;{0fdf1895-6f9e-42ce-8d58-388c0ab8356d}))")
	iidAccelHandler = pinterfaceIID("pinterface({9de1c534-6ae1-11e0-84e1-18a905bcc53f};" +
		"rc(Windows.Devices.Sensors.Accelerometer;{df184548-2711-4da7-8098-4b82205d3c7d});" +
		"rc(Windows.Devices.Sensors.AccelerometerReadingChangedEventArgs;{0095c65b-b6ac-475a-9f44-8b32d35a3f25}))")
)

// Vtable slots past IInspectable's six (QueryInterface, AddRef, Release,
// GetIids, GetRuntimeClassName, GetTrustLevel).
const (
	slotQueryInterface    = 0
	slotRelease           = 2
	slotGetDefault        = 6  // I*Statics
	slotMinimumInterval   = 7  // IGyrometer, IAccelerometer
	slotPutReportInterval = 8  // ...
	slotAddReadingChanged = 10 // ...
	slotRemoveReading     = 11 // ...
	slotArgsReading       = 6  // I*ReadingChangedEventArgs.Reading
	slotTimestamp         = 6  // I*Reading: Timestamp, then X, Y, Z
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

// pinterfaceIID is the IID Windows gives a parameterized interface from its
// signature: a name-based (SHA-1) UUID in the Windows Runtime's namespace.
func pinterfaceIID(sig string) windows.GUID {
	ns := []byte{0x11, 0xf4, 0x7a, 0xd5, 0x7b, 0x73, 0x42, 0xc0, 0xab, 0xae, 0x87, 0x8b, 0x1e, 0x16, 0xad, 0xee}
	h := sha1.Sum(append(ns, sig...))
	h[6] = h[6]&0x0f | 0x50
	h[8] = h[8]&0x3f | 0x80
	g := windows.GUID{Data1: binary.BigEndian.Uint32(h[0:]), Data2: binary.BigEndian.Uint16(h[4:]), Data3: binary.BigEndian.Uint16(h[6:])}
	copy(g.Data4[:], h[8:16])
	return g
}

// comObject is a Windows Runtime object: a pointer to its vtable.
type comObject struct{ vtbl *[16]uintptr }

func (o *comObject) call(slot int, args ...uintptr) uint32 {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return uint32(r)
}

func (o *comObject) release() {
	if o != nil {
		o.call(slotRelease)
	}
}

// reading is one report of a sensor: its time (Windows' 100 ns ticks) and values.
type reading struct {
	ticks   int64
	x, y, z float64
}

// motionSource gives the newest gyroscope and accelerometer readings, and
// says on updated when a new gyroscope reading came.
type motionSource interface {
	read() (gyro, accel reading, ok bool)
	updated() <-chan struct{}
	close()
}

// ---- the event handlers (static: Windows keeps pointers to them)

type delegate struct {
	vtbl *[4]uintptr
	iid  *windows.GUID
}

var (
	delegateVtbl  [4]uintptr
	gyroDelegate  = delegate{vtbl: &delegateVtbl, iid: &iidGyroHandler}
	accelDelegate = delegate{vtbl: &delegateVtbl, iid: &iidAccelHandler}

	latestMu   sync.Mutex
	latestGyro reading
	latestAcc  reading
	haveGyro   bool
	haveAcc    bool
	gyroSignal = make(chan struct{}, 1)
)

func init() {
	delegateVtbl = [4]uintptr{
		syscall.NewCallback(delegateQueryInterface),
		syscall.NewCallback(func(*delegate) uintptr { return 1 }), // AddRef (static object)
		syscall.NewCallback(func(*delegate) uintptr { return 1 }), // Release
		syscall.NewCallback(delegateInvoke),
	}
}

func delegateFor(this *delegate) *delegate {
	if this == &gyroDelegate || this == &accelDelegate {
		return this
	}
	return nil
}

func delegateQueryInterface(this *delegate, iid *windows.GUID, out **delegate) uintptr {
	d := delegateFor(this)
	if d != nil && (*iid == iidUnknown || *iid == iidAgileObject || *iid == *d.iid) {
		*out = this
		return 0
	}
	*out = nil
	return 0x80004002 // E_NOINTERFACE
}

// delegateInvoke is ReadingChanged(sender, args), on one of Windows' threads.
func delegateInvoke(this *delegate, sender, args *comObject) uintptr {
	d := delegateFor(this)
	if d == nil || args == nil {
		return 0
	}
	var r *comObject
	if hr := args.call(slotArgsReading, uintptr(unsafe.Pointer(&r))); hr != 0 || r == nil {
		return 0
	}
	var v reading
	r.call(slotTimestamp, uintptr(unsafe.Pointer(&v.ticks)))
	r.call(slotTimestamp+1, uintptr(unsafe.Pointer(&v.x)))
	r.call(slotTimestamp+2, uintptr(unsafe.Pointer(&v.y)))
	r.call(slotTimestamp+3, uintptr(unsafe.Pointer(&v.z)))
	r.release()
	latestMu.Lock()
	if d == &gyroDelegate {
		latestGyro, haveGyro = v, true
	} else {
		latestAcc, haveAcc = v, true
	}
	latestMu.Unlock()
	if d == &gyroDelegate {
		select {
		case gyroSignal <- struct{}{}:
		default:
		}
	}
	return 0
}

// ---- the sensors

type winSensors struct {
	gyro, accel           *comObject
	gyroToken, accelToken int64
}

// openWinSensors opens the gyroscope and the accelerometer at their fastest
// rate. One at a time; the calling thread must be in the multithreaded
// apartment (roInitialize) until close.
func openWinSensors() (*winSensors, error) {
	g, err := defaultSensor("Windows.Devices.Sensors.Gyrometer", &iidGyrometerStatics)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("This PC has no gyroscope Windows can read.")
	}
	a, err := defaultSensor("Windows.Devices.Sensors.Accelerometer", &iidAccelerometerStatics)
	if err != nil || a == nil {
		g.release()
		if err == nil {
			err = errors.New("This PC has no accelerometer Windows can read.")
		}
		return nil, err
	}
	latestMu.Lock()
	haveGyro, haveAcc = false, false
	latestMu.Unlock()
	w := &winSensors{gyro: g, accel: a}
	for _, s := range []struct {
		obj   *comObject
		d     *delegate
		token *int64
	}{{g, &gyroDelegate, &w.gyroToken}, {a, &accelDelegate, &w.accelToken}} {
		var min uint32
		s.obj.call(slotMinimumInterval, uintptr(unsafe.Pointer(&min)))
		if min == 0 {
			min = 10
		}
		s.obj.call(slotPutReportInterval, uintptr(min))
		if hr := s.obj.call(slotAddReadingChanged, uintptr(unsafe.Pointer(s.d)), uintptr(unsafe.Pointer(s.token))); hr != 0 {
			w.close()
			return nil, errors.New("Couldn't listen to the motion sensor: " + windows.Errno(hr).Error())
		}
	}
	return w, nil
}

func defaultSensor(class string, iid *windows.GUID) (*comObject, error) {
	name, err := windows.UTF16FromString(class)
	if err != nil {
		return nil, err
	}
	var hs uintptr
	if r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)-1), uintptr(unsafe.Pointer(&hs))); r != 0 {
		return nil, windows.Errno(r)
	}
	defer procWindowsDeleteString.Call(hs)
	var statics *comObject
	if r, _, _ := procRoGetActivationFactory.Call(hs, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&statics))); r != 0 {
		return nil, errors.New("Windows' sensor service isn't available: " + windows.Errno(r).Error())
	}
	defer statics.release()
	var s *comObject
	if hr := statics.call(slotGetDefault, uintptr(unsafe.Pointer(&s))); hr != 0 {
		return nil, windows.Errno(hr)
	}
	return s, nil
}

func (w *winSensors) read() (gyro, accel reading, ok bool) {
	latestMu.Lock()
	defer latestMu.Unlock()
	return latestGyro, latestAcc, haveGyro && haveAcc
}

func (w *winSensors) updated() <-chan struct{} { return gyroSignal }

func (w *winSensors) close() {
	if w.gyroToken != 0 {
		w.gyro.call(slotRemoveReading, uintptr(w.gyroToken))
	}
	if w.accelToken != 0 {
		w.accel.call(slotRemoveReading, uintptr(w.accelToken))
	}
	w.gyro.release()
	w.accel.release()
}

// roInitialize puts the calling (locked) thread in the multithreaded apartment.
func roInitialize() (func(), error) {
	r, _, _ := procRoInitialize.Call(1) // RO_INIT_MULTITHREADED
	switch {
	case uint32(r) == 0x80010106: // RPC_E_CHANGED_MODE: initialized already, usable
		return func() {}, nil
	case int32(r) < 0:
		return nil, windows.Errno(r)
	}
	return func() { procRoUninitialize.Call() }, nil
}

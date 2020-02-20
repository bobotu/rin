package rin

import (
	"runtime"
	"sync/atomic"
	"unsafe"
)

type channel struct {
	slots []channelSlot
}

func (c *channel) Init() {
	c.slots = make([]channelSlot, runtime.GOMAXPROCS(0))
}

func (c *channel) Send(i channelItem) {
	pid := procPin()
	c.slots[pid].push(i)
	procUnpin()
}

func (c *channel) Reap(reaper func(items []channelItem)) {
	for i := range c.slots {
		reaper(c.slots[i].reap())
	}
}

type channelItem struct {
	op       uint8
	bufIdx   uint16
	fd       int32
	userData uint64
	addr     uint64
	off      uint64
	len      uint32
}

type channelSlot struct {
	channelSlotInner
	// Avoid false sharing between different slot.
	pad [128 - slotSize%128]byte
}

const slotSize = unsafe.Sizeof(channelSlotInner{})

type channelSlotInner struct {
	// We cannot use sync.Mutex with runtime.procPin, if so the scheduler will paniced.
	// Because the critical region is small and nearly no contention, use a simple spin lock instead.
	lock     int32
	writable []channelItem
	readonly []channelItem
}

func (s *channelSlot) reap() []channelItem {
	s.Lock()
	s.writable, s.readonly = s.readonly[:0], s.writable
	s.Unlock()
	return s.readonly
}

func (s *channelSlot) Lock() {
	for {
		if atomic.CompareAndSwapInt32(&s.lock, 0, 1) {
			return
		}
	}
}

func (s *channelSlot) Unlock() {
	atomic.StoreInt32(&s.lock, 0)
}

func (s *channelSlot) push(i channelItem) {
	s.Lock()
	s.writable = append(s.writable, i)
	s.Unlock()
}

//go:linkname procPin runtime.procPin
func procPin() int

//go:linkname procUnpin runtime.procUnpin
func procUnpin() int

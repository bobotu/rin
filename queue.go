package rin

import (
	"fmt"
	"reflect"
	"runtime"
	"sync/atomic"
	"unsafe"
)

type submitQueue struct {
	// fields reference kernel memory.
	khead        *uint32
	ktail        *uint32
	kringMask    *uint32
	kringEntries *uint32
	kflags       *uint32
	kdropped     *uint32
	karray       []uint32
	ksqes        []sqe

	head uint32
	tail uint32

	ringMmap []byte
	sqesMmap []byte
}

func newSubmitQueue(fd int32, p *params) (*submitQueue, error) {
	sz := p.sqOff.array + (p.sqEntries * 4)
	ringData, err := mmap(fd, offSqRing, int(sz))
	if err != nil {
		return nil, err
	}
	ringPtr := unsafe.Pointer(&ringData[0])

	sqesSz := int(uintptr(p.sqEntries) * unsafe.Sizeof(sqe{}))
	sqesData, err := mmap(fd, offSqes, sqesSz)
	if err != nil {
		unmap(ringData)
		return nil, err
	}

	sq := &submitQueue{
		khead:        (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.head))),
		ktail:        (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.tail))),
		kringMask:    (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.ringMask))),
		kringEntries: (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.ringEntries))),
		kflags:       (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.flags))),
		kdropped:     (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.dropped))),
		ringMmap:     ringData,
		sqesMmap:     sqesData,
	}
	arrayHdr := (*reflect.SliceHeader)(unsafe.Pointer(&sq.karray))
	arrayHdr.Cap = int(p.sqEntries)
	arrayHdr.Len = int(p.sqEntries)
	arrayHdr.Data = uintptr(ringPtr) + uintptr(p.sqOff.array)

	sqesHdr := (*reflect.SliceHeader)(unsafe.Pointer(&sq.ksqes))
	sqesHdr.Cap = int(p.sqEntries)
	sqesHdr.Len = int(p.sqEntries)
	sqesHdr.Data = uintptr(unsafe.Pointer(&sqesData[0]))

	return sq, nil
}

func (sq *submitQueue) TryPopSqe(flags uint32) *sqe {
	next := sq.tail + 1
	head := sq.head
	if flags&setupSqPoll != 0 {
		head = atomic.LoadUint32(sq.khead)
	}

	if int(next-head) > len(sq.ksqes) {
		return nil
	}

	idx := sq.tail & *sq.kringMask
	sq.tail = next
	return &sq.ksqes[idx]
}

func (sq *submitQueue) Flush() uint32 {
	mask := *sq.kringMask
	toSubmit := sq.tail - sq.head
	ktail := atomic.LoadUint32(sq.ktail)

	for i := uint32(0); i < toSubmit; i++ {
		idx := ktail & mask
		atomic.StoreUint32(&sq.karray[idx], sq.head&mask)
		ktail++
		sq.head++
	}
	atomic.StoreUint32(sq.ktail, ktail)
	return toSubmit
}

func (sq *submitQueue) SubmitAll(fd int32, flags uint32) int {
	cnt := sq.Flush()

	if flags&setupSqPoll == 0 {
		remain := cnt
		for remain > 0 {
			sbmt, err := enter(fd, remain, 0, flags)
			if err != nil {
				panic(fmt.Sprintf("submit request entries failed, this should never occur. err: %s", err))
			}
			remain -= uint32(sbmt)
		}
		return int(cnt)
	}

	if atomic.LoadUint32(sq.kflags)&sqNeedWakeup != 0 {
		// the kernel has signalled to us that the
		// SQPOLL thread that checks the submission
		// queue has terminated due to inactivity,
		// and needs to be restarted.
		if _, err := enter(fd, cnt, 0, enterSqWakeup); err != nil {
			panic(fmt.Sprintf("wakeup kernel poll thread failed, this should never occur. err: %s", err))
		}
	}
	return 0
}

func (sq *submitQueue) Destroy() {
	unmap(sq.ringMmap)
	unmap(sq.sqesMmap)
	*sq = submitQueue{}
}

type completionQueue struct {
	khead     *uint32
	ktail     *uint32
	kringMask *uint32
	koverflow *uint32
	kcqes     []cqe

	fd      int32
	tickets []Ticket

	ringMmap []byte
}

func newCompletionQueue(fd int32, p *params, tickets []Ticket) (*completionQueue, error) {
	sz := p.cqOff.cqes + p.cqEntries*uint32(unsafe.Sizeof(cqe{}))
	ringData, err := mmap(fd, offCqRing, int(sz))
	if err != nil {
		return nil, err
	}
	ringPtr := unsafe.Pointer(&ringData[0])

	cq := &completionQueue{
		khead:     (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.cqOff.head))),
		ktail:     (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.cqOff.tail))),
		kringMask: (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.cqOff.ringMask))),
		koverflow: (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.cqOff.overflow))),

		fd:       fd,
		tickets:  tickets,
		ringMmap: ringData,
	}
	cqesHdr := (*reflect.SliceHeader)(unsafe.Pointer(&cq.kcqes))
	cqesHdr.Cap = int(p.cqEntries)
	cqesHdr.Len = int(p.cqEntries)
	cqesHdr.Data = uintptr(ringPtr) + uintptr(p.cqOff.cqes)

	return cq, nil
}

func (cq *completionQueue) Destroy() {
	unmap(cq.ringMmap)
}

func (cq *completionQueue) Reaper() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for {
		if _, err := enter(cq.fd, 0, 1, enterGetEvents); err != nil {
			panic(fmt.Sprintf("error in cqe reaper: %s", err))
		}
		stop := cq.reaper()
		if stop {
			return
		}
	}
}

func (cq *completionQueue) reaper() bool {
	head := atomic.LoadUint32(cq.khead)
	tail := atomic.LoadUint32(cq.ktail)
	var stop bool

	for head != tail {
		idx := head & *cq.kringMask
		e := &cq.kcqes[idx]

		if e.userData&poisonPillMask != 0 {
			e.userData &= (poisonPillMask - 1)
			stop = true
		}

		ticket := &cq.tickets[e.userData]
		ticket.res = e.res
		ticket.wg.Done()

		atomic.AddUint32(cq.khead, 1)
		head++
	}
	return stop
}

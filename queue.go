package rin

import (
	"fmt"
	"reflect"
	"runtime"
	"sync"
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

	freeSqes      []uint64
	freeSqeHead   uint64
	enqueueLock   uint32
	submittedTail uint32
	submitLock    sync.Mutex

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

	freeSqes := make([]uint64, p.sqEntries)
	for i := range freeSqes {
		freeSqes[i] = uint64(i + 1)
	}

	sq := &submitQueue{
		khead:        (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.head))),
		ktail:        (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.tail))),
		kringMask:    (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.ringMask))),
		kringEntries: (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.ringEntries))),
		kflags:       (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.flags))),
		kdropped:     (*uint32)(unsafe.Pointer(uintptr(ringPtr) + uintptr(p.sqOff.dropped))),
		freeSqes:     freeSqes,
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

const lockBit = uint64(1) << 63

func (sq *submitQueue) TryGetSqe(flags uint32) (*sqe, int) {
	var head uint64
	for {
		head = atomic.LoadUint64(&sq.freeSqeHead)
		if head&lockBit != 0 {
			continue
		}
		if head >= uint64(len(sq.ksqes)) {
			return nil, -1
		}
		if atomic.CompareAndSwapUint64(&sq.freeSqeHead, head, head|lockBit) {
			break
		}
	}
	next := sq.freeSqes[head]
	atomic.StoreUint64(&sq.freeSqeHead, next)
	return &sq.ksqes[head], int(head)
}

func (sq *submitQueue) Enqueue(sqe int) uint32 {
	for {
		if atomic.CompareAndSwapUint32(&sq.enqueueLock, 0, 1) {
			break
		}
	}
	mask := *sq.kringMask
	ktail := atomic.LoadUint32(sq.ktail)
	idx := ktail & mask
	atomic.StoreUint32(&sq.karray[idx], uint32(sqe)&mask)
	atomic.StoreUint32(sq.ktail, ktail+1)
	atomic.StoreUint32(&sq.enqueueLock, 0)
	return ktail
}

func (sq *submitQueue) SubmitAll(fd int32, flags uint32) {
	last := atomic.LoadUint32(sq.ktail) - 1
	sq.EnsureSqeSubmitted(last, fd, flags)
}

func (sq *submitQueue) EnsureSqeSubmitted(pos uint32, fd int32, flags uint32) {
	if flags&setupSqPoll != 0 {
		if atomic.LoadUint32(sq.kflags)&sqNeedWakeup != 0 {
			// the kernel has signalled to us that the
			// SQPOLL thread that checks the submission
			// queue has terminated due to inactivity,
			// and needs to be restarted.
			if _, err := enter(fd, 0, 0, enterSqWakeup); err != nil {
				panic(fmt.Sprintf("wakeup kernel poll thread failed, this should never occur. err: %s", err))
			}
		}
		khead := atomic.LoadUint32(sq.khead)
		prevSubmitted := atomic.SwapUint32(&sq.submittedTail, khead)
		if prevSubmitted != khead {
			sq.releaseSqes(prevSubmitted, khead)
		}
		return
	}

	submitted := atomic.LoadUint32(&sq.submittedTail)
	if submitted > pos {
		return
	}
	sq.submitLock.Lock()
	submitted = atomic.LoadUint32(&sq.submittedTail)
	if submitted > pos {
		sq.submitLock.Unlock()
		return
	}

	ktail := atomic.LoadUint32(sq.ktail)
	remain := ktail - submitted
	for remain > 0 {
		sbmt, err := enter(fd, remain, 0, 0)
		if err != nil {
			panic(fmt.Sprintf("submit request entries failed, this should never occur. err: %s", err))
		}
		remain -= uint32(sbmt)
	}
	atomic.StoreUint32(&sq.submittedTail, ktail)
	sq.releaseSqes(submitted, ktail)
	sq.submitLock.Unlock()
}

func (sq *submitQueue) releaseSqes(from, to uint32) {
	sqes := make([]uint64, 0, to-from)
	mask := *sq.kringMask

	for i := from; i < to; i++ {
		sqe := atomic.LoadUint32(&sq.karray[i&mask])
		sqes = append(sqes, uint64(sqe))
	}
	for i := 0; i < len(sqes)-1; i++ {
		sq.freeSqes[sqes[i]] = uint64(sqes[i+1])
	}

	tail := sqes[len(sqes)-1]
	var oldHead uint64
	for {
		oldHead = atomic.LoadUint64(&sq.freeSqeHead)
		if oldHead&lockBit != 0 {
			continue
		}
		if atomic.CompareAndSwapUint64(&sq.freeSqeHead, oldHead, oldHead|lockBit) {
			break
		}
	}
	sq.freeSqes[tail] = oldHead
	atomic.StoreUint64(&sq.freeSqeHead, uint64(sqes[0]))
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

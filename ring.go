package rin

import (
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

type Ticket struct {
	wg    sync.WaitGroup
	res   int32
	reqId int64
	id    int
}

func (t *Ticket) reset(id int, reqId int64) {
	*t = Ticket{
		res:   0,
		id:    id,
		reqId: reqId,
	}
	t.wg.Add(1)
}

type Config struct {
	QueueDepth              uint32
	SubmitQueuePollAffinity uint32
	SubmitQueuePollMode     bool
}

type Ring struct {
	ticketCh chan int
	tickets  []Ticket

	fd    int32
	flags uint32

	iovecs []unix.Iovec
	sq     struct {
		sync.Mutex
		*submitQueue
	}
	cq *completionQueue

	requestId   int64
	submittedId int64
}

func NewRing(conf *Config) (*Ring, error) {
	p := new(params)
	if conf.SubmitQueuePollMode {
		p.flags |= setupSqPoll
		p.sqThreadCpu = conf.SubmitQueuePollAffinity
	}

	fd, err := setup(conf.QueueDepth, p)
	if err != nil {
		return nil, err
	}
	if fd < 0 {
		panic("invalid ring fd")
	}

	capacity := p.cqEntries
	ticketCh := make(chan int, capacity)
	for i := 0; i < int(capacity); i++ {
		ticketCh <- i
	}

	r := &Ring{
		ticketCh: ticketCh,
		tickets:  make([]Ticket, capacity),

		fd:    fd,
		flags: p.flags,

		requestId:   0,
		submittedId: -1,
	}
	iovecs, err := alloc(int(capacity) * int(unsafe.Sizeof(unix.Iovec{})))
	if err != nil {
		r.Close()
		return nil, err
	}
	hdr := (*reflect.SliceHeader)(unsafe.Pointer(&r.iovecs))
	hdr.Cap = int(capacity)
	hdr.Len = int(capacity)
	hdr.Data = uintptr(unsafe.Pointer(&iovecs[0]))

	r.sq.submitQueue, err = newSubmitQueue(fd, p)
	if err != nil {
		r.Close()
		return nil, err
	}

	r.cq, err = newCompletionQueue(fd, p, r.tickets)
	if err != nil {
		r.Close()
		return nil, err
	}
	go r.cq.Reaper()

	return r, nil
}

func (r *Ring) Close() {
	if r.sq.submitQueue != nil && r.cq != nil {
		r.stopReaper()
	}

	if r.sq.submitQueue != nil {
		r.sq.Lock()
		r.sq.Destroy()
		r.sq.Unlock()
	}
	if r.cq != nil {
		r.cq.Destroy()
	}
	if len(r.iovecs) != 0 {
		var b []byte
		hdr := (*reflect.SliceHeader)(unsafe.Pointer(&b))
		sz := len(r.iovecs) * int(unsafe.Sizeof(unix.Iovec{}))
		hdr.Cap = sz
		hdr.Len = sz
		hdr.Data = uintptr(unsafe.Pointer(&r.iovecs[0]))
		free(b)
	}
	os.NewFile(uintptr(r.fd), "ring").Close()
}

const poisonPillMask = (uint64(1) << 63)

func (r *Ring) stopReaper() {
	t := r.withSqe(nil, false, func(e *sqe) {
		e.opcode = opNop
		e.fd = -1
		e.flags = sqeIoDrain
		e.userData |= poisonPillMask
	})
	r.Await(t)
}

func (r *Ring) Await(t *Ticket) (int32, error) {
	r.ensureSubmitted(t.reqId)
	t.wg.Wait()
	res := t.res
	r.ticketCh <- t.id

	if res < 0 {
		return 0, unix.Errno(-res)
	}
	return res, nil
}

func (r *Ring) Nop() *Ticket {
	return r.withSqe(nil, false, func(e *sqe) {
		e.opcode = opNop
		e.fd = -1
		e.flags = 0
	})
}

func (r *Ring) ReadAt(f *os.File, buf []byte, offset int) *Ticket {
	return r.withSqe(buf, true, func(e *sqe) {
		e.opcode = opReadv
		e.fd = int32(f.Fd())
		e.offOrAddr2 = uint64(offset)
		e.flags = 0
	})
}

func (r *Ring) WriteAt(f *os.File, buf []byte, offset int) *Ticket {
	return r.withSqe(buf, true, func(e *sqe) {
		e.opcode = opWritev
		e.fd = int32(f.Fd())
		e.offOrAddr2 = uint64(offset)
		e.flags = 0
	})
}

func (r *Ring) SubmitAll() {
	r.sq.Lock()
	defer r.sq.Unlock()
	r.sq.SubmitAll(r.fd, r.flags)
}

func (r *Ring) setIovec(e *sqe, buf []byte, id int) {
	vec := &r.iovecs[id]
	vec.Base = &buf[0]
	vec.Len = uint64(len(buf))
	e.addr = uint64(uintptr(unsafe.Pointer(vec)))
	e.len = 1
}

func (r *Ring) withSqe(buf []byte, iovec bool, f func(e *sqe)) *Ticket {
	tidx := <-r.ticketCh
	t := &r.tickets[tidx]

	r.sq.Lock()
	defer r.sq.Unlock()

	t.reset(tidx, r.requestId)
	r.requestId++

	var e *sqe
	for {
		e = r.sq.TryPopSqe(r.flags)
		if e != nil {
			break
		}
		r.submittedId += int64(r.sq.SubmitAll(r.fd, r.flags))
	}

	e.userData = uint64(tidx)
	if len(buf) != 0 {
		if iovec {
			r.setIovec(e, buf, tidx)
		} else {
			e.addr = uint64(uintptr(unsafe.Pointer(&buf[0])))
			e.len = uint32(len(buf))
		}
	}
	f(e)

	return t
}

func (r *Ring) ensureSubmitted(reqId int64) {
	current := atomic.LoadInt64(&r.submittedId)
	if current >= reqId {
		return
	}

	r.sq.Lock()
	defer r.sq.Unlock()
	current = atomic.LoadInt64(&r.submittedId)
	if current >= reqId {
		return
	}

	submitted := int64(r.sq.SubmitAll(r.fd, r.flags))
	current = atomic.AddInt64(&r.submittedId, submitted)
	if r.flags&setupSqPoll == 0 && current < reqId {
		panic("there are some bug in submit queue")
	}
}

package rin

import (
	"reflect"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

type contextRegister struct {
	sync.Mutex
	ctxs []Context
	next int

	iovecs    []unix.Iovec
	iovecsPtr []byte
	page      []byte
	pagePtr   []byte
}

func (cr *contextRegister) Init(fd int32, size int) error {
	var err error
	cr.iovecsPtr, err = alloc(size * int(unsafe.Sizeof(unix.Iovec{})))
	if err != nil {
		return err
	}
	hdr := (*reflect.SliceHeader)(unsafe.Pointer(&cr.iovecs))
	hdr.Cap = size
	hdr.Len = size
	hdr.Data = uintptr(unsafe.Pointer(&cr.iovecsPtr[0]))

	cr.page, cr.pagePtr, err = allocPages(size)
	if err != nil {
		cr.Destroy()
		return err
	}

	cr.ctxs = make([]Context, size)
	for i := range cr.ctxs {
		ctx := &cr.ctxs[i]
		ctx.id = uint32(i + 1)
		buf := cr.page[i*pageSize : (i+1)*pageSize]
		ctx.buf = buf
		ctx.bufIdx = i
		cr.iovecs[i].Len = pageSize
		cr.iovecs[i].Base = &buf[0]
	}

	if err := register(fd, registerBuffers, unsafe.Pointer(&cr.iovecs[0]), size); err != nil {
		cr.Destroy()
		return err
	}

	return nil
}

func (cr *contextRegister) Destroy() {
	if cr.iovecsPtr != nil {
		free(cr.iovecsPtr)
		cr.iovecs = nil
	}
	if cr.pagePtr != nil {
		free(cr.pagePtr)
		cr.pagePtr = nil
	}
}

func (cr *contextRegister) Acquire() *Context {
	cr.Lock()
	defer cr.Unlock()
	if cr.next >= len(cr.ctxs) {
		return nil
	}

	ctx := &cr.ctxs[cr.next]
	ctx.id, cr.next = uint32(cr.next), int(ctx.id)
	return ctx
}

func (cr *contextRegister) Release(ctx *Context) {
	cr.Lock()
	defer cr.Unlock()
	ctx.id, cr.next = uint32(cr.next), int(ctx.id)
}

func (cr *contextRegister) Get(id uint32) *Context {
	return &cr.ctxs[id]
}

type Context struct {
	wg  sync.WaitGroup
	res int32

	// used as next free index in free-list
	id     uint32
	bufIdx int
	buf    []byte
}

func (ctx *Context) GetBuffer(size int) []byte {
	return ctx.buf[:size]
}

func (ctx *Context) getBufferAddr() uint64 {
	return uint64(uintptr(unsafe.Pointer(&ctx.buf[0])))
}

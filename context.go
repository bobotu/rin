package rin

import "sync"

type contextRegister struct {
	sync.Mutex
	ctxs []Context
	next int
}

func (cr *contextRegister) Init(size int) {
	cr.ctxs = make([]Context, size)
	for i := range cr.ctxs {
		cr.ctxs[i].id = uint32(i + 1)
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
	id uint32
}

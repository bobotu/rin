package main

import (
	"runtime"
	"sync/atomic"
	"time"

	_ "net/http/pprof"

	"github.com/bobotu/rin"
)

type counter struct {
	cnt  int64
	_pad [8]byte
}

func (c *counter) Add(i int) {
	atomic.AddInt64(&c.cnt, int64(i))
}

func main() {
	ringIo()
}

func ringIo() {
	ring, err := rin.NewRing(&rin.Config{
		QueueDepth:          4096,
		SubmitQueuePollMode: true,

		MaxContexts:   10240,
		MaxLoopNoReq:  10000,
		MaxLoopNoResp: 10000,
	})
	if err != nil {
		panic(err)
	}

	proc := runtime.GOMAXPROCS(0)
	counters := make([]counter, proc)
	for i := 0; i < proc; i++ {
		go func(idx int) {
			ctx := make([]*rin.Context, 64)
			for i := range ctx {
				ctx[i] = ring.NewContext()
			}
			for {
				for _, c := range ctx {
					ring.Nop(c)
				}
				for _, c := range ctx {
					if _, err := ring.Await(c); err != nil {
						panic(err)
					}
				}
				counters[idx].Add(len(ctx))
			}
		}(i)
	}

	sum := func() int64 {
		var result int64
		for i := range counters {
			result += atomic.LoadInt64(&counters[i].cnt)
		}
		return result
	}
	ticker := time.NewTicker(3 * time.Second)
	prev := int64(0)
	for {
		<-ticker.C
		curr := sum()
		println((curr-prev)/3, "op/s")
		prev = curr
	}
}

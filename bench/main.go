package main

import (
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	_ "net/http/pprof"

	"github.com/bobotu/rin"
	"golang.org/x/sys/unix"
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
	// normalIo()
}

func normalIo() {
	proc := runtime.GOMAXPROCS(0)
	counters := make([]counter, proc)
	for i := 0; i < proc; i++ {
		go func(idx int) {
			f, err := ioutil.TempFile("", "fixture-*")
			if err != nil {
				panic(err)
			}
			buf := alignedBlock()

			rand.Read(buf)
			if _, err := f.Write(buf); err != nil {
				panic(err)
			}
			name := f.Name()
			f.Close()
			f, err = os.OpenFile(name, unix.O_DIRECT|unix.O_RDWR, 0666)
			if err != nil {
				panic(err)
			}
			for {
				if _, err := f.ReadAt(buf, 0); err != nil {
					panic(err)
				}
				counters[idx].Add(1)
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

func ringIo() {
	ring, err := rin.NewRing(&rin.Config{
		QueueDepth:          4096,
		SubmitQueuePollMode: true,
	})
	if err != nil {
		panic(err)
	}

	proc := runtime.GOMAXPROCS(0)
	counters := make([]counter, proc)
	for i := 0; i < proc; i++ {
		go func(idx int) {
			f, err := ioutil.TempFile("", "fixture-*")
			if err != nil {
				panic(err)
			}
			buf := alignedBlock()

			rand.Read(buf)
			if _, err := f.Write(buf); err != nil {
				panic(err)
			}
			name := f.Name()
			f.Close()
			f, err = os.OpenFile(name, unix.O_DIRECT|unix.O_RDWR, 0666)
			if err != nil {
				panic(err)
			}
			for {
				if _, err := ring.Await(ring.ReadAt(f, buf, 0)); err != nil {
					panic(err)
				}
				counters[idx].Add(1)
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

func alignedBlock() []byte {
	block, err := unix.Mmap(-1, 0, 4096*2, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS|unix.MAP_NORESERVE)
	if err != nil {
		panic(err)
	}
	a := alignment(block, 4096)
	offset := 0
	if a != 0 {
		offset = 4096 - a
	}
	block = block[offset : offset+4096]
	// Can't check alignment of a zero sized block
	if 4096 != 0 {
		a = alignment(block, 4096)
		if a != 0 {
			log.Fatal("Failed to align block")
		}
	}
	return block
}

func alignment(block []byte, alignSize int) int {
	return int(uintptr(unsafe.Pointer(&block[0])) & uintptr(alignSize-1))
}

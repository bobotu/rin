package rin

import (
	"bytes"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
)

func TestAwaitAndStop(t *testing.T) {
	ring, err := NewRing(&Config{
		QueueDepth:          32,
		SubmitQueuePollMode: false,
		MaxContexts:         8,
		MaxLoopNoReq:        2,
		MaxLoopNoResp:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Close()

	ctx := ring.NewContext()
	defer ring.Finish(ctx)

	ring.Nop(ctx)
	if _, err := ring.Await(ctx); err != nil {
		t.Fatal(err)
	}

	ring.Nop(ctx)
	if _, err := ring.Await(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReadWritePage(t *testing.T) {
	content := make([]byte, 4096)
	rand.Read(content)

	ring, err := NewRing(&Config{
		QueueDepth:          32,
		SubmitQueuePollMode: false,
		MaxContexts:         8,
		MaxLoopNoReq:        2,
		MaxLoopNoResp:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Close()

	ctx := ring.NewContext()
	defer ring.Finish(ctx)

	writeAndReadFile(t, ctx, ring, content)
}

func TestSocket(t *testing.T) {
	l, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 60336,
	})
	if err != nil {
		t.Fatal(err)
	}

	ring, err := NewRing(&Config{
		QueueDepth:          32,
		SubmitQueuePollMode: false,
		MaxContexts:         4,
		MaxLoopNoReq:        2,
		MaxLoopNoResp:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Close()

	go func() {
		for {
			conn, err := l.AcceptTCP()
			if err != nil {
				return
			}

			go func() {
				fd, err := conn.File()
				if err != nil {
					t.Fatal(err)
				}
				conn.Close()
				defer fd.Close()

				ctx := ring.NewContext()
				defer ring.Finish(ctx)

				ring.ReadAt(ctx, int32(fd.Fd()), 4096, 0)
				sz, err := ring.Await(ctx)
				if err != nil {
					t.Fatal(err)
				}
				log.Printf("read value from client conn %v\n", ctx.GetBuffer(int(sz)))

				buf := ctx.GetBuffer(int(sz))
				for i := range buf {
					buf[i]++
				}

				ring.WriteAt(ctx, int32(fd.Fd()), int(sz), 0)
				written, err := ring.Await(ctx)
				if err != nil {
					t.Fatal(err)
				}
				log.Printf("written %d to client\n", written)
			}()
		}
	}()

	conn, err := net.DialTCP("tcp", nil, &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 60336,
	})
	if err != nil {
		t.Fatal(err)
	}
	fd, err := conn.File()
	if err != nil {
		t.Fatal(err)
	}

	ctx := ring.NewContext()
	defer ring.Finish(ctx)

	buf := ctx.GetBuffer(256)
	rand.Read(buf)

	ring.WriteAt(ctx, int32(fd.Fd()), len(buf), 0)
	written, err := ring.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	log.Printf("written %d to server\n", written)

	ring.ReadAt(ctx, int32(fd.Fd()), len(buf), 0)
	sz, err := ring.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sz == 0 {
		t.Fatal("read EOF")
	}
	log.Printf("read value from server %v\n", ctx.GetBuffer(int(sz)))
	fd.Close()
	l.Close()
}

func TestConcurrentReadWrite(t *testing.T) {
	t.Skip("not for now")
	var wg sync.WaitGroup
	start := make(chan struct{})

	ring, err := NewRing(&Config{
		QueueDepth:          32,
		SubmitQueuePollMode: false,
		MaxContexts:         8,
		MaxLoopNoReq:        2,
		MaxLoopNoResp:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Close()

	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := ring.NewContext()
			defer ring.Finish(ctx)
			content := make([]byte, 128*4096)
			rand.Read(content)
			<-start
			writeAndReadFile(t, ctx, ring, content)
		}()
	}
	close(start)
	wg.Wait()
}

func writeAndReadFile(t *testing.T, ctx *Context, ring *Ring, content []byte) {
	f, err := ioutil.TempFile("", "fixture*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	fd := int32(f.Fd())
	buf := ctx.GetBuffer(len(content))
	copy(buf, content)

	ring.WriteAt(ctx, fd, len(content), 0)
	nn, err := ring.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if int(nn) != len(content) {
		t.Fatal("not write")
	}

	copy(buf, make([]byte, len(content)))
	ring.ReadAt(ctx, fd, len(content), 0)
	_, err = ring.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(ctx.GetBuffer(len(content)), content) {
		t.Fatalf("content is incorrect")
	}

}

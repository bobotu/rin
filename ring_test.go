package rin

import (
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
	ring.Nop(ctx)
	if _, err := ring.Await(ctx); err != nil {
		t.Fatal(err)
	}

	ring.Nop(ctx)
	if _, err := ring.Await(ctx); err != nil {
		t.Fatal(err)
	}
}

// func TestReadWritePage(t *testing.T) {
// 	content := make([]byte, 4096)
// 	rand.Read(content)

// 	ring, err := NewRing(&Config{
// 		QueueDepth:          32,
// 		SubmitQueuePollMode: false,
// 	})
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	defer ring.Close()

// 	writeAndReadFile(t, ring, content)
// }

// func TestConcurrentReadWrite(t *testing.T) {
// 	var wg sync.WaitGroup
// 	start := make(chan struct{})

// 	ring, err := NewRing(&Config{
// 		QueueDepth:          32,
// 		SubmitQueuePollMode: false,
// 	})
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	defer ring.Close()

// 	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
// 		wg.Add(1)
// 		go func() {
// 			defer wg.Done()
// 			content := make([]byte, 128*4096)
// 			rand.Read(content)
// 			<-start
// 			writeAndReadFile(t, ring, content)
// 		}()
// 	}
// 	close(start)
// 	wg.Wait()
// }

// func writeAndReadFile(t *testing.T, ring *Ring, content []byte) {
// 	f, err := ioutil.TempFile("", "fixture*")
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	defer os.Remove(f.Name())

// 	w := NewWriter(ring, f)
// 	nn, err := w.Write(content)
// 	if nn != len(content) {
// 		t.Fatal(err)
// 	}
// 	if err := w.Flush(); err != nil {
// 		t.Fatal(err)
// 	}

// 	if _, err := f.Seek(0, io.SeekStart); err != nil {
// 		t.Fatal(err)
// 	}

// 	r := NewReader(ring, f)
// 	result, err := ioutil.ReadAll(r)
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	if len(result) != len(content) {
// 		t.Fatalf("size incorrect %d != %d", len(result), len(content))
// 	}

// 	if !bytes.Equal(result, content) {
// 		t.Fatalf("content is incorrect")
// 	}

// }

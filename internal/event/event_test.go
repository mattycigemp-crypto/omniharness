package event

import (
	"sync"
	"testing"
	"time"
)

func TestNewAndDecode(t *testing.T) {
	e := New(&TaskCreatedData{Prompt: "hello"})
	if e.Type != TaskCreated {
		t.Fatalf("type = %q, want task.created", e.Type)
	}
	if e.ID == "" {
		t.Fatal("missing id")
	}
	p, err := Decode(e)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := p.(*TaskCreatedData)
	if !ok {
		t.Fatalf("decoded type %T", p)
	}
	if d.Prompt != "hello" {
		t.Fatalf("prompt = %q", d.Prompt)
	}
}

func TestDecodeUnknownType(t *testing.T) {
	e := Event{Type: "nope", Data: []byte("{}")}
	if _, err := Decode(e); err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestBusFanout(t *testing.T) {
	b := NewBus()
	defer b.Close()
	ch1, c1 := b.Subscribe(16)
	ch2, c2 := b.Subscribe(16)
	defer c1()
	defer c2()

	b.Publish(New(&TaskCreatedData{Prompt: "x"}))

	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case e := <-ch:
			if e.Type != TaskCreated {
				t.Fatalf("got %q", e.Type)
			}
		case <-time.After(time.Second):
			t.Fatal("event not delivered")
		}
	}
}

func TestBusFilter(t *testing.T) {
	b := NewBus()
	defer b.Close()
	ch, cancel := b.SubscribeTo(8, TaskCreated)
	defer cancel()

	b.Publish(New(&TaskCreatedData{}))
	b.Publish(New(&TaskCompletedData{}))

	select {
	case e := <-ch:
		if e.Type != TaskCreated {
			t.Fatalf("got %q", e.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("expected TaskCreated")
	}
	select {
	case e := <-ch:
		t.Fatalf("unexpected %q", e.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBusDropOldest(t *testing.T) {
	b := NewBus()
	defer b.Close()
	ch, cancel := b.Subscribe(2)
	defer cancel()

	// Fill the 2-slot buffer without consuming.
	b.Publish(New(&TaskCreatedData{Prompt: "1"}))
	b.Publish(New(&TaskCreatedData{Prompt: "2"}))
	b.Publish(New(&TaskCreatedData{Prompt: "3"}))
	b.Publish(New(&TaskCreatedData{Prompt: "4"}))

	// Oldest should have been dropped; newest survive.
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case e := <-ch:
			d, _ := Decode(e)
			got[d.(*TaskCreatedData).Prompt] = true
		case <-time.After(time.Second):
			t.Fatal("buffer underfilled")
		}
	}
	if !got["3"] || !got["4"] {
		t.Fatalf("expected newest kept, got %v", got)
	}
}

func TestBusCancelStopsDelivery(t *testing.T) {
	b := NewBus()
	defer b.Close()
	ch, cancel := b.Subscribe(4)
	cancel()

	b.Publish(New(&TaskCreatedData{}))
	select {
	case e, ok := <-ch:
		if ok {
			t.Fatalf("received after cancel: %v", e)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("channel not closed after cancel")
	}
}

func TestBusPublishAfterClose(t *testing.T) {
	b := NewBus()
	ch, _ := b.Subscribe(4)
	b.Close()
	b.Publish(New(&TaskCreatedData{})) // must not panic

	select {
	case e, ok := <-ch:
		if ok {
			t.Fatalf("received event after close: %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed")
	}
}

func TestSlowConsumerDoesNotBlock(t *testing.T) {
	b := NewBus()
	defer b.Close()
	_, cancel := b.Subscribe(1)
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			b.Publish(New(&TaskCreatedData{}))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher blocked on slow consumer")
	}
}

func TestStressPublishNeverBlocks(t *testing.T) {
	b := NewBus()
	// One fast subscriber, one that never drains (worst case for backpressure).
	fastCh, _ := b.Subscribe(64)
	slowCh, _ := b.Subscribe(1)
	go func() {
		for range fastCh {
		}
	}()
	_ = slowCh

	// A publisher hammering from many goroutines must never block, even with
	// a full slow subscriber: the bus drops the oldest event instead.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5000; i++ {
			b.Publish(New(&TaskCreatedData{Prompt: "x"}))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
}

func TestConcurrentPublish(t *testing.T) {
	b := NewBus()
	defer b.Close()
	ch, cancel := b.Subscribe(1024)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ch:
			case <-time.After(100 * time.Millisecond):
				return
			}
		}
	}()

	const workers = 8
	const each = 500
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				b.Publish(New(&TaskCreatedData{}))
			}
		}()
	}
	wg.Wait()
	<-done
}

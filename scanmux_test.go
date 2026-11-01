package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScanMuxSharesOneScan(t *testing.T) {
	var starts atomic.Int32
	stopped := make(chan struct{}, 4)
	run := func(ctx context.Context, emit func(Advert)) error {
		starts.Add(1)
		emit(Advert{ID: "A"})
		<-ctx.Done()
		stopped <- struct{}{}
		return nil
	}
	m := newScanMux(run)

	var got1, got2 atomic.Int32
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.Scan(ctx1, func(Advert) { got1.Add(1) }) }()
	eventually(t, "first subscriber to get an advert", func() bool { return got1.Load() >= 1 })
	go func() { defer wg.Done(); m.Scan(ctx2, func(Advert) { got2.Add(1) }) }()
	// the late subscriber gets what was already seen (BlueZ would not repeat it)
	eventually(t, "late subscriber to get the replay", func() bool { return got2.Load() >= 1 })

	if n := starts.Load(); n != 1 {
		t.Fatalf("underlying scan started %d times, want 1", n)
	}
	cancel1()
	select {
	case <-stopped:
		t.Fatal("scan stopped while a subscriber was left")
	case <-time.After(50 * time.Millisecond):
	}
	cancel2()
	wg.Wait()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not stop after the last subscriber left")
	}

	// a new subscriber starts a fresh scan
	ctx3, cancel3 := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Scan(ctx3, func(Advert) {}) }()
	eventually(t, "second scan start", func() bool { return starts.Load() == 2 })
	cancel3()
	<-done
}

func TestScanMuxReportsFailure(t *testing.T) {
	boom := errors.New("adapter off")
	m := newScanMux(func(ctx context.Context, emit func(Advert)) error { return boom })
	err := m.Scan(context.Background(), func(Advert) {})
	if !errors.Is(err, boom) {
		t.Fatalf("Scan error = %v, want %v", err, boom)
	}
}

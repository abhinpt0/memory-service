package testbarrier

import (
	"sync"
	"testing"
	"time"
)

// Barrier synchronizes a fixed number of goroutines at a deterministic test point.
type Barrier struct {
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

func New(parties int) *Barrier {
	return &Barrier{
		arrived: make(chan struct{}, parties),
		release: make(chan struct{}),
	}
}

// Wait blocks a participant until the test releases every participant.
func (b *Barrier) Wait() {
	b.arrived <- struct{}{}
	<-b.release
}

// WaitForParties waits until every participant reached Wait, then releases them together.
func (b *Barrier) WaitForParties(t testing.TB, parties int) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for range parties {
		select {
		case <-b.arrived:
		case <-timer.C:
			b.once.Do(func() { close(b.release) })
			t.Fatalf("timed out waiting for %d test barrier participants", parties)
		}
	}
	b.once.Do(func() { close(b.release) })
}

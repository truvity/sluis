package memo

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestKeyOfKeepsPartBoundaries(t *testing.T) {
	if KeyOf([]byte("ab"), []byte("c")) == KeyOf([]byte("a"), []byte("bc")) {
		t.Fatal("different parts hash alike")
	}
}

func TestDoComputesOnceAndRemembersTheError(t *testing.T) {
	c := New[int](4)
	var calls atomic.Int32
	boom := errors.New("boom")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Do(KeyOf([]byte("k")), func() (int, error) { calls.Add(1); return 7, boom })
			if v != 7 || !errors.Is(err, boom) {
				t.Errorf("got %d, %v", v, err)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("computed %d times", n)
	}
}

func TestDoIsBounded(t *testing.T) {
	c := New[int](2)
	for i := 0; i < 5; i++ {
		_, _ = c.Do(KeyOf([]byte{byte(i)}), func() (int, error) { return i, nil })
	}
	if len(c.items) != 2 || len(c.order) != 2 {
		t.Fatalf("holds %d/%d", len(c.items), len(c.order))
	}
}

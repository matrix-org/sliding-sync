package internal

import (
	"sync"
	"testing"
	"time"
)

// Test basic functions of WorkerPool
func TestWorkerPool(t *testing.T) {
	wp := NewWorkerPool(2)
	wp.Start()
	defer wp.Stop()

	// we should process this concurrently as N=2 so it should take 1s not 2s
	var wg sync.WaitGroup
	wg.Add(2)
	start := time.Now()
	wp.Queue(func() {
		time.Sleep(time.Second)
		wg.Done()
	})
	wp.Queue(func() {
		time.Sleep(time.Second)
		wg.Done()
	})
	wg.Wait()
	took := time.Since(start)
	if took > 2*time.Second {
		t.Fatalf("took %v for queued work, it should have been faster than 2s", took)
	}
}

func TestWorkerPoolDoesWorkPriorToStart(t *testing.T) {
	wp := NewWorkerPool(2)

	// return channel to use to see when work is done
	ch := make(chan int, 2)
	wp.Queue(func() {
		ch <- 1
	})
	wp.Queue(func() {
		ch <- 2
	})

	// the work should not be done yet
	time.Sleep(100 * time.Millisecond)
	if len(ch) > 0 {
		t.Fatalf("Queued work was done before Start()")
	}

	// the work should be starting now
	wp.Start()
	defer wp.Stop()

	sum := 0
	for {
		select {
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for work to be done")
		case val := <-ch:
			sum += val
		}
		if sum == 3 { // 2 + 1
			break
		}
	}
}

type workerState struct {
	id      int
	state   int             // not running, queued, running, finished
	unblock *sync.WaitGroup // decrement to unblock this worker
}

func TestWorkerPoolBackpressure(t *testing.T) {
	// this test assumes backpressure starts at n*2+1 due to a chan buffer of size n, and n in-flight work.
	n := 2
	wp := NewWorkerPool(n)
	wp.Start()
	defer wp.Stop()

	var mu sync.Mutex
	stateNotRunning := 0
	stateQueued := 1
	stateRunning := 2
	stateFinished := 3
	size := (2 * n) + 1
	running := make([]*workerState, size)

	go func() {
		// we test backpressure by scheduling (n*2)+1 work and ensuring that we see the following running states:
		// [2,2,1,1,0] <-- 2 running, 2 queued, 1 blocked <-- THIS IS BACKPRESSURE
		// [3,2,2,1,1] <-- 1 finished, 2 running, 2 queued
		// [3,3,2,2,1] <-- 2 finished, 2 running , 1 queued
		// [3,3,3,2,2] <-- 3 finished, 2 running
		for i := 0; i < size; i++ {
			// set initial state of this piece of work
			wg := &sync.WaitGroup{}
			wg.Add(1)
			state := &workerState{
				id:      i,
				state:   stateNotRunning,
				unblock: wg,
			}
			mu.Lock()
			running[i] = state
			mu.Unlock()

			// queue the work on the pool. The final piece of work will block here and remain in
			// stateNotRunning and not transition to stateQueued until the first piece of work is done.
			wp.Queue(func() {
				mu.Lock()
				if running[state.id].state != stateQueued {
					// we ran work in the worker faster than the code underneath .Queue, so let it catch up
					mu.Unlock()
					time.Sleep(10 * time.Millisecond)
					mu.Lock()
				}
				running[state.id].state = stateRunning
				mu.Unlock()

				running[state.id].unblock.Wait()
				mu.Lock()
				running[state.id].state = stateFinished
				mu.Unlock()
			})

			// mark this work as queued
			mu.Lock()
			running[i].state = stateQueued
			mu.Unlock()
		}
	}()

	// wait for the workers to be doing work and assert the states of each task
	time.Sleep(time.Second)

	assertStates(t, &mu, running, []int{
		stateRunning, stateRunning, stateQueued, stateQueued, stateNotRunning,
	})

	// now let the first task complete
	running[0].unblock.Done()
	// wait for the pool to grab more work
	time.Sleep(100 * time.Millisecond)
	// assert new states
	assertStates(t, &mu, running, []int{
		stateFinished, stateRunning, stateRunning, stateQueued, stateQueued,
	})

	// now let the second task complete
	running[1].unblock.Done()
	// wait for the pool to grab more work
	time.Sleep(100 * time.Millisecond)
	// assert new states
	assertStates(t, &mu, running, []int{
		stateFinished, stateFinished, stateRunning, stateRunning, stateQueued,
	})

	// now let the third task complete
	running[2].unblock.Done()
	// wait for the pool to grab more work
	time.Sleep(100 * time.Millisecond)
	// assert new states
	assertStates(t, &mu, running, []int{
		stateFinished, stateFinished, stateFinished, stateRunning, stateRunning,
	})

}

func assertStates(t *testing.T, mu *sync.Mutex, running []*workerState, wantStates []int) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if len(running) != len(wantStates) {
		t.Fatalf("assertStates: bad wantStates length, got %d want %d", len(wantStates), len(running))
	}
	for i := range running {
		state := running[i]
		wantVal := wantStates[i]
		if state.state != wantVal {
			t.Errorf("work[%d] got state %d want %d", i, state.state, wantVal)
		}
	}
}

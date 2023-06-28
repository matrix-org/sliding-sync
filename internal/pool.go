package internal

type WorkerPool struct {
	N  int
	ch chan func()
}

// Create a new worker pool of size N. Up to N work can be done concurrently.
// The size of N depends on the expected frequency of work and contention for
// shared resources. Large values of N allow more frequent work at the cost of
// more contention for shared resources like cpu, memory and fds. Small values
// of N allow less frequent work but control the amount of shared resource contention.
// Ideally this value will be derived from whatever shared resource constraints you
// are hitting up against, rather than set to a fixed value. For example, if you have
// a database connection limit of 100, then setting N to some fraction of the limit is
// preferred to setting this to an arbitrary number < 100. If more than N work is requested,
// eventually WorkerPool.Queue will block until some work is done.
//
// The larger N is, the larger the up front memory costs are due to the implementation of WorkerPool.
func NewWorkerPool(n int) *WorkerPool {
	return &WorkerPool{
		N: n,
		// If we have N workers, we can process N work concurrently.
		// If we have >N work, we need to apply backpressure to stop us
		// making more and more work which takes up more and more memory.
		// By setting the channel size to N, we ensure that backpressure is
		// being applied on the producer, stopping it from creating more work,
		// and hence bounding memory consumption. Work is still being produced
		// upstream on the homeserver, but we will consume it when we're ready
		// rather than gobble it all at once.
		//
		// Note: we aren't forced to set this to N, it just serves as a useful
		// metric which scales on the number of workers. The amount of in-flight
		// work is N, so it makes sense to allow up to N work to be queued up before
		// applying backpressure. If the channel buffer is < N then the channel can
		// become the bottleneck in the case where we have lots of instantaneous work
		// to do. If the channel buffer is too large, we needlessly consume memory as
		// make() will allocate a backing array of whatever size you give it up front (sad face)
		ch: make(chan func(), n),
	}
}

// Start the workers. Only call this once.
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.N; i++ {
		go wp.worker()
	}
}

// Stop the worker pool. Only really useful for tests as a worker pool should be started once
// and persist for the lifetime of the process, else it causes needless goroutine churn.
// Only call this once.
func (wp *WorkerPool) Stop() {
	close(wp.ch)
}

// Queue some work on the pool. May or may not block until some work is processed.
func (wp *WorkerPool) Queue(fn func()) {
	wp.ch <- fn
}

// worker impl
func (wp *WorkerPool) worker() {
	for fn := range wp.ch {
		fn()
	}
}

package internal

import (
	"os"
	"testing"
)

func TestAssertion(t *testing.T) {
	os.Setenv("SYNCV3_DEBUG", "1")
	shouldPanic := true
	shouldNotPanic := false

	try(t, shouldNotPanic, func() {
		Assert("true does nothing", true)
	})
	try(t, shouldPanic, func() {
		Assert("false panics", false)
	})

	os.Setenv("SYNCV3_DEBUG", "0")
	try(t, shouldNotPanic, func() {
		Assert("true does nothing", true)
	})
	try(t, shouldNotPanic, func() {
		Assert("false does not panic if SYNCV3_DEBUG is not 1", false)
	})
}

func try(t *testing.T, shouldPanic bool, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		err := recover()
		if err != nil {
			if shouldPanic {
				return
			}
			t.Fatalf("panic: %s", err)
		} else {
			if shouldPanic {
				t.Fatalf("function did not panic")
			}
		}
	}()
	fn()
}

package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/getsentry/sentry-go"
	"github.com/rs/zerolog/log"
)

type HandlerError struct {
	StatusCode int
	Err        error
	ErrCode    string
}

func (e *HandlerError) Error() string {
	return fmt.Sprintf("HTTP %d : %s", e.StatusCode, e.Err.Error())
}

func (e *HandlerError) Unwrap() error {
	return e.Err
}

type jsonError struct {
	Err  string `json:"error"`
	Code string `json:"errcode,omitempty"`
}

func (e HandlerError) JSON() []byte {
	je := jsonError{
		Err:  e.Error(),
		Code: e.ErrCode,
	}
	b, _ := json.Marshal(je)
	return b
}

func ExpiredSessionError() *HandlerError {
	return &HandlerError{
		StatusCode: 400,
		Err:        fmt.Errorf("session expired"),
		ErrCode:    "M_UNKNOWN_POS",
	}
}

// Assert that the expression is true, similar to assert() in C. If expr is false, print or panic.
//
// If expr is false and SYNCV3_DEBUG=1 then the program panics.
// If expr is false and SYNCV3_DEBUG is unset or not '1' then the program logs an error along with
// a field which contains the file/line number of the caller/assertion of Assert.
// Assert should be used to verify invariants which should never be broken during normal functioning
// of the program, and shouldn't be used to log a normal error e.g network errors. Developers can
// make use of this function by setting SYNCV3_DEBUG=1 when running the server, which will fail-fast
// whenever a programming or logic error occurs.
// If expr is false and SYNCV3_SENTRY_DSN is configured, an error event is sent to Sentry, including
// the msg verbatim.
//
// The msg provided should be the expectation of the assert e.g:
//
//	Assert("list is not empty", len(list) > 0)
//
// Which then produces:
//
//	assertion failed: list is not empty
//
// An optional debugContext map can be provided. If it is present and sentry is configured,
// it is added as context to the sentry events generated for failed assertions.
func Assert(msg string, expr bool, debugContext ...map[string]interface{}) {
	assert(msg, expr)
	if !expr {
		sentry.WithScope(func(scope *sentry.Scope) {
			if len(debugContext) > 0 {
				scope.SetContext(SentryCtxKey, debugContext[0])
			}
			sentry.CaptureException(fmt.Errorf("assertion failed: %s", msg))
		})
	}
}

// AssertWithContext is a version of Assert that associates any sentry events with a
// request context.
func AssertWithContext(ctx context.Context, msg string, expr bool) {
	assert(msg, expr)
	if !expr {
		GetSentryHubFromContextOrDefault(ctx).CaptureException(fmt.Errorf("assertion failed: %s", msg))
	}
}

func assert(msg string, expr bool) {
	if expr {
		return
	}
	if os.Getenv("SYNCV3_DEBUG") == "1" {
		panic(fmt.Sprintf("assert: %s", msg))
	}
	l := log.Error()
	_, file, line, ok := runtime.Caller(1)
	if ok {
		l = l.Str("assertion", fmt.Sprintf("%s:%d", file, line))
	}
	_, file, line, ok = runtime.Caller(2)
	if ok {
		l = l.Str("caller", fmt.Sprintf("%s:%d", file, line))
	}
	l.Msg("assertion failed: " + msg)
}

// DataError indicates that this error is caused by invalid data, so retrying
// this request will not make any difference. This is used by the poller to hint
// at whether or not we should advance the since token on error.
type DataError struct {
	msg string
}

func (e *DataError) Error() string {
	return e.msg
}

func NewDataError(msg string, args ...interface{}) error {
	return &DataError{
		msg: fmt.Sprintf("DataError: "+msg, args...),
	}
}

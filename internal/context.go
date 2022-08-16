package internal

import (
	"context"

	"github.com/rs/zerolog"
)

type ctx string

var (
	ctxData ctx = "syncv3_data"
)

// logging metadata for a single request
type data struct {
	userID   string
	since    int64
	next     int64
	numRooms int
	txnID    string
}

// prepare a request context so it can contain syncv3 info
func RequestContext(ctx context.Context) context.Context {
	d := &data{
		since:    -1,
		next:     -1,
		numRooms: -1,
	}
	return context.WithValue(ctx, ctxData, d)
}

// add the user ID to this request context. Need to have called RequestContext first.
func SetRequestContextUserID(ctx context.Context, userID string) {
	d := ctx.Value(ctxData)
	if d == nil {
		return
	}
	da := d.(*data)
	da.userID = userID
}

func SetRequestContextResponseInfo(ctx context.Context, since, next int64, numRooms int, txnID string) {
	d := ctx.Value(ctxData)
	if d == nil {
		return
	}
	da := d.(*data)
	da.since = since
	da.next = next
	da.numRooms = numRooms
	da.txnID = txnID
}

func DecorateLogger(ctx context.Context, l *zerolog.Event) *zerolog.Event {
	d := ctx.Value(ctxData)
	if d == nil {
		return l
	}
	da := d.(*data)
	if da.userID != "" {
		l = l.Str("u", da.userID)
	}
	if da.since >= 0 {
		l = l.Int64("p", da.since)
	}
	if da.next >= 0 {
		l = l.Int64("q", da.next)
	}
	if da.txnID != "" {
		l = l.Str("t", da.txnID)
	}
	if da.numRooms >= 0 {
		l = l.Int("r", da.numRooms)
	}
	return l
}

package sync3

import (
	"encoding/json"
	"strconv"

	"github.com/matrix-org/sync-v3/sync3/extensions"
	"github.com/tidwall/gjson"
)

const (
	OpSync       = "SYNC"
	OpInvalidate = "INVALIDATE"
	OpInsert     = "INSERT"
	OpDelete     = "DELETE"
	OpUpdate     = "UPDATE"
)

type Response struct {
	Ops     []ResponseOp `json:"ops"`
	Initial bool         `json:"initial,omitempty"`

	RoomSubscriptions map[string]Room `json:"room_subscriptions"`
	Counts            []int           `json:"counts"`

	Extensions extensions.Response `json:"extensions"`

	Pos     string `json:"pos"`
	Session string `json:"session_id,omitempty"`
}

func (r *Response) PosInt() int64 {
	p, _ := strconv.ParseInt(r.Pos, 10, 64)
	return p
}

// Custom unmarshal so we can dynamically create the right ResponseOp for Ops
func (r *Response) UnmarshalJSON(b []byte) error {
	temporary := struct {
		Ops []json.RawMessage `json:"ops"`

		RoomSubscriptions map[string]Room     `json:"room_subscriptions"`
		Counts            []int               `json:"counts"`
		Extensions        extensions.Response `json:"extensions"`

		Pos     string `json:"pos"`
		Initial bool   `json:"initial"`
		Session string `json:"session_id,omitempty"`
	}{}
	if err := json.Unmarshal(b, &temporary); err != nil {
		return err
	}
	r.RoomSubscriptions = temporary.RoomSubscriptions
	r.Counts = temporary.Counts
	r.Pos = temporary.Pos
	r.Initial = temporary.Initial
	r.Session = temporary.Session
	r.Extensions = temporary.Extensions

	for _, op := range temporary.Ops {
		if gjson.GetBytes(op, "range").Exists() {
			var oper ResponseOpRange
			if err := json.Unmarshal(op, &oper); err != nil {
				return err
			}
			r.Ops = append(r.Ops, &oper)
		} else {
			var oper ResponseOpSingle
			if err := json.Unmarshal(op, &oper); err != nil {
				return err
			}
			r.Ops = append(r.Ops, &oper)
		}
	}

	return nil
}

type ResponseOp interface {
	Op() string
}

type ResponseOpRange struct {
	Operation string  `json:"op"`
	List      int     `json:"list"`
	Range     []int64 `json:"range,omitempty"`
	Rooms     []Room  `json:"rooms,omitempty"`
}

func (r *ResponseOpRange) Op() string {
	return r.Operation
}

type ResponseOpSingle struct {
	Operation string `json:"op"`
	List      int    `json:"list"`
	Index     *int   `json:"index,omitempty"` // 0 is a valid value, hence *int
	Room      *Room  `json:"room,omitempty"`
}

func (r *ResponseOpSingle) Op() string {
	return r.Operation
}

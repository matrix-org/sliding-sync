package sync3

import (
	"encoding/json"
	"strconv"

	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/tidwall/gjson"
)

const (
	OpSync       = "SYNC"
	OpInvalidate = "INVALIDATE"
	OpInsert     = "INSERT"
	OpDelete     = "DELETE"
)

type Response struct {
	Lists []ResponseList `json:"lists"`

	Rooms      map[string]Room     `json:"rooms"`
	Extensions extensions.Response `json:"extensions"`

	Pos        string `json:"pos"`
	TxnID      string `json:"txn_id,omitempty"`
	DeltaToken string `json:"delta_token,omitempty"`
	Session    string `json:"session_id,omitempty"`
}

type ResponseList struct {
	Ops   []ResponseOp `json:"ops,omitempty"`
	Count int          `json:"count"`
}

func (r *Response) PosInt() int64 {
	p, _ := strconv.ParseInt(r.Pos, 10, 64)
	return p
}

func (r *Response) ListOps() int {
	num := 0
	for _, l := range r.Lists {
		if len(l.Ops) > 0 {
			num += len(l.Ops)
		}
	}
	return num
}

// Custom unmarshal so we can dynamically create the right ResponseOp for Ops
func (r *Response) UnmarshalJSON(b []byte) error {
	temporary := struct {
		Rooms map[string]Room `json:"rooms"`
		Lists []struct {
			Ops   []json.RawMessage `json:"ops"`
			Count int               `json:"count"`
		} `json:"lists"`
		Extensions extensions.Response `json:"extensions"`

		Pos     string `json:"pos"`
		TxnID   string `json:"txn_id,omitempty"`
		Session string `json:"session_id,omitempty"`
	}{}
	if err := json.Unmarshal(b, &temporary); err != nil {
		return err
	}
	r.Rooms = temporary.Rooms
	r.Pos = temporary.Pos
	r.TxnID = temporary.TxnID
	r.Session = temporary.Session
	r.Extensions = temporary.Extensions
	r.Lists = make([]ResponseList, len(temporary.Lists))

	for i := range temporary.Lists {
		l := temporary.Lists[i]
		var list ResponseList
		list.Count = l.Count
		for _, op := range l.Ops {
			if gjson.GetBytes(op, "range").Exists() {
				var oper ResponseOpRange
				if err := json.Unmarshal(op, &oper); err != nil {
					return err
				}
				list.Ops = append(list.Ops, &oper)
			} else {
				var oper ResponseOpSingle
				if err := json.Unmarshal(op, &oper); err != nil {
					return err
				}
				list.Ops = append(list.Ops, &oper)
			}
		}
		r.Lists[i] = list
	}

	return nil
}

type ResponseOp interface {
	Op() string
	// which rooms are we giving data about
	IncludedRoomIDs() []string
}

type ResponseOpRange struct {
	Operation string   `json:"op"`
	Range     []int64  `json:"range,omitempty"`
	RoomIDs   []string `json:"room_ids,omitempty"`
}

func (r *ResponseOpRange) Op() string {
	return r.Operation
}
func (r *ResponseOpRange) IncludedRoomIDs() []string {
	if r.Op() == OpInvalidate {
		return nil // the rooms are being excluded
	}
	return r.RoomIDs
}

type ResponseOpSingle struct {
	Operation string `json:"op"`
	Index     *int   `json:"index,omitempty"` // 0 is a valid value, hence *int
	RoomID    string `json:"room_id,omitempty"`
}

func (r *ResponseOpSingle) Op() string {
	return r.Operation
}

func (r *ResponseOpSingle) IncludedRoomIDs() []string {
	if r.Op() == OpDelete || r.RoomID == "" {
		return nil // the room is being excluded
	}
	return []string{r.RoomID}
}

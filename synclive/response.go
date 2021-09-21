package synclive

type Response struct {
	Ops []ResponseOp `json:"ops"`

	RoomSubscriptions map[string]ResponseOp `json:"room_subscriptions"`
	Count             int64                 `json:"count"`
	Notifications     bool                  `json:"notifications"`
}

type ResponseOp interface {
	Op() string
}

type ResponseOpRange struct {
	Operation string `json:"op"`
	Range     []int  `json:"range,omitempty"`
	Rooms     []Room `json:"rooms,omitempty"`
}

func (r *ResponseOpRange) Op() string {
	return r.Operation
}

type ResponseOpSingle struct {
	Operation string `json:"op"`
	Index     *int   `json:"index,omitempty"` // 0 is a valid value, hence *int
	Room      *Room  `json:"room,omitempty"`
}

func (r *ResponseOpSingle) Op() string {
	return r.Operation
}

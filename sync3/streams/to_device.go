package streams

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
)

const (
	defaultToDeviceMessageLimit = 100
	maxToDeviceMessageLimit     = 1000
)

type FilterToDevice struct {
	Limit int64 `json:"limit"`
}

func (f *FilterToDevice) Combine(other *FilterToDevice) *FilterToDevice {
	combined := &FilterToDevice{
		Limit: f.Limit,
	}
	if other.Limit != 0 {
		combined.Limit = other.Limit
	}
	return combined
}

type ToDeviceResponse struct {
	Limit  int64             `json:"limit"`
	Events []json.RawMessage `json:"events"`
}

// ToDevice represents a stream of to_device messages.
type ToDevice struct {
	storage *state.Storage
}

func NewToDevice(s *state.Storage) *ToDevice {
	return &ToDevice{s}
}

func (s *ToDevice) Position(tok *sync3.Token) int64 {
	return tok.ToDevicePosition()
}

func (s *ToDevice) SetPosition(tok *sync3.Token, pos int64) {
	tok.SetToDevicePosition(pos)
}

func (s *ToDevice) SessionConfirmed(session *sync3.Session, confirmedPos int64, allSessions bool) {
	if !allSessions {
		return
	}
	_ = s.storage.ToDeviceTable.DeleteMessagesUpToAndIncluding(session.DeviceID, confirmedPos)
}

func (s *ToDevice) DataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	if request.ToDevice == nil {
		return 0, ErrNotRequested
	}
	// limit negotiation
	negotiatedLimit := request.ToDevice.Limit
	if request.ToDevice.Limit == 0 {
		request.ToDevice.Limit = defaultToDeviceMessageLimit
		negotiatedLimit = defaultToDeviceMessageLimit
	} else if request.ToDevice.Limit > maxToDeviceMessageLimit {
		request.ToDevice.Limit = maxToDeviceMessageLimit
		negotiatedLimit = maxToDeviceMessageLimit
	}
	msgs, upTo, err := s.storage.ToDeviceTable.Messages(session.DeviceID, fromExcl, toIncl, request.ToDevice.Limit)
	if err != nil {
		return 0, err
	}
	resp.ToDevice = &ToDeviceResponse{
		Limit:  negotiatedLimit,
		Events: msgs,
	}
	return upTo, nil
}

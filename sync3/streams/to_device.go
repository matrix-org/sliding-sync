package streams

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
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

func (s *ToDevice) DataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) error {
	if request.ToDevice == nil {
		return ErrNotRequested
	}
	msgs, err := s.storage.ToDeviceTable.Messages(session.DeviceID, fromExcl, toIncl)
	if err != nil {
		return err
	}
	resp.ToDevice = &ToDeviceResponse{
		Limit:  request.ToDevice.Limit,
		Events: msgs,
	}
	return nil
}

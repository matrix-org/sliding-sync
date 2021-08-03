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

func (s *ToDevice) Process(session *sync3.Session, from, to int64, f *FilterToDevice, resp *ToDeviceResponse) error {
	msgs, err := s.storage.ToDeviceTable.Messages(session.DeviceID, from, to)
	if err != nil {
		return err
	}
	resp.Limit = f.Limit
	resp.Events = msgs
	return nil
}

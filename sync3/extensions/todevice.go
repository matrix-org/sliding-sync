package extensions

import "encoding/json"

// Client created request params
type ToDeviceRequest struct {
	Enabled bool   `json:"enabled"`
	Limit   int    `json:"limit"` // max number of to-device messages per response
	Since   string `json:"since"` // since token
}

// Server response
type ToDeviceResponse struct {
	NextBatch string            `json:"next_batch"`
	Events    []json.RawMessage `json:"events"`
}

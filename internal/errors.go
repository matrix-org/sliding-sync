package internal

import (
	"encoding/json"
	"fmt"
)

type HandlerError struct {
	StatusCode int
	Err        error
}

func (e *HandlerError) Error() string {
	return fmt.Sprintf("HTTP %d : %s", e.StatusCode, e.Err.Error())
}

type jsonError struct {
	Err string `json:"error"`
}

func (e HandlerError) JSON() []byte {
	je := jsonError{e.Error()}
	b, _ := json.Marshal(je)
	return b
}

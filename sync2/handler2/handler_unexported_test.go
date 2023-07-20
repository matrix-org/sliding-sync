package handler2

import (
	"encoding/json"
	"testing"
)

func Test_typingHash(t *testing.T) {
	tests := []struct {
		name     string
		ephEvent json.RawMessage
		want     uint64
	}{
		{
			name:     "doesn't fall over if list is empty",
			ephEvent: json.RawMessage(`{"content":{"user_ids":[]}}`),
			want:     14695981039346656037,
		},
		{
			name:     "hash alice typing",
			ephEvent: json.RawMessage(`{"content":{"user_ids":["@alice:localhost"]}}`),
			want:     16709353342265369358,
		},
		{
			name:     "hash alice and bob typing",
			ephEvent: json.RawMessage(`{"content":{"user_ids":["@alice:localhost","@bob:localhost"]}}`),
			want:     11071889279173799154,
		},
		{
			name:     "hash bob and alice typing",
			ephEvent: json.RawMessage(`{"content":{"user_ids":["@bob:localhost","@alice:localhost"]}}`),
			want:     11071889279173799154, // this should be the same as the previous
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := typingHash(tt.ephEvent); got != tt.want {
				t.Errorf("typingHash() = %v, want %v", got, tt.want)
			}
		})
	}
}

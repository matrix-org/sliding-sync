package streams

type Response struct {
	Next   string          `json:"next"`
	Typing *TypingResponse `json:"typing,omitempty"`
}

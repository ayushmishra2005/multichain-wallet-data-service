package zerion

import "fmt"

// Kind classifies an upstream failure for local HTTP mapping.
type Kind int

const (
	KindBadRequest Kind = iota
	KindAuth
	KindNotFound
	KindUnprocessable
	KindRateLimited
	KindPending
	KindUnavailable
	KindTimeout
	KindCanceled
	KindInvalidResponse
	KindBusy
)

// Error is a classified Zerion client failure. It does not include
// upstream error detail or URLs.
type Error struct {
	Kind   Kind
	Status int
	msg    string
}

func (e *Error) Error() string {
	if e == nil {
		return "zerion: empty error"
	}
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("zerion: %s", e.Kind)
}

func (k Kind) String() string {
	switch k {
	case KindBadRequest:
		return "bad_request"
	case KindAuth:
		return "auth"
	case KindNotFound:
		return "not_found"
	case KindUnprocessable:
		return "unprocessable"
	case KindRateLimited:
		return "rate_limited"
	case KindPending:
		return "pending"
	case KindUnavailable:
		return "unavailable"
	case KindTimeout:
		return "timeout"
	case KindCanceled:
		return "canceled"
	case KindInvalidResponse:
		return "invalid_response"
	case KindBusy:
		return "busy"
	default:
		return "unknown"
	}
}

func (e *Error) Result() string {
	if e == nil {
		return "unknown"
	}
	switch e.Kind {
	case KindBadRequest, KindUnprocessable:
		return "bad_request"
	case KindAuth:
		return "auth_error"
	case KindRateLimited:
		return "rate_limited"
	case KindPending:
		return "pending"
	case KindTimeout:
		return "timeout"
	case KindCanceled:
		return "canceled"
	case KindInvalidResponse, KindNotFound:
		return "decode_error"
	case KindBusy:
		return "busy"
	default:
		return "server_error"
	}
}

func wrapKind(k Kind, status int, msg string) *Error {
	return &Error{Kind: k, Status: status, msg: msg}
}

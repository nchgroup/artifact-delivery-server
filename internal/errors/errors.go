package errors

import "fmt"

type Error struct {
	Status  int
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Message != "" && e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "application error"
}

func (e *Error) Unwrap() error { return e.Err }

func New(status int, format string, args ...any) error {
	return &Error{Status: status, Message: fmt.Sprintf(format, args...)}
}

func Wrap(status int, err error, format string, args ...any) error {
	return &Error{Status: status, Message: fmt.Sprintf(format, args...), Err: err}
}

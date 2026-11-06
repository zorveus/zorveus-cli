package problem

import "errors"

type Error struct {
	Code     string  `json:"code"`
	Message  string  `json:"message"`
	ExitCode int     `json:"-"`
	Pointer  *string `json:"pointer,omitempty"`
}

func (err *Error) Error() string {
	return err.Message
}

func New(code, message string, exitCode int) *Error {
	return &Error{Code: code, Message: message, ExitCode: exitCode}
}

func Safe(err error) *Error {
	var known *Error
	if errors.As(err, &known) {
		return known
	}
	return New("operation_failed", "The command failed. Try again.", 1)
}

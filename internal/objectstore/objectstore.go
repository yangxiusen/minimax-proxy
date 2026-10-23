package objectstore

import "context"

type Store interface {
	UploadFile(context.Context, string, string, string) (string, error)
}

type DataStore interface {
	Upload(context.Context, []byte, string, string) (string, error)
}

type Error struct {
	Code       string
	Message    string
	Retryable  bool
	Operation  string
	HTTPStatus int
	Reason     string
	RequestID  string
	Cause      error
}

func (err *Error) Error() string          { return err.Message }
func (err *Error) Unwrap() error          { return err.Cause }
func (err *Error) SafeLogMessage() string { return err.Message }

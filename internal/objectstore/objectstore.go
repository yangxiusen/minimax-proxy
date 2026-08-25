package objectstore

import "context"

type Store interface {
	UploadFile(context.Context, string, string, string) (string, error)
}

type Error struct {
	Code      string
	Message   string
	Retryable bool
}

func (err *Error) Error() string { return err.Message }

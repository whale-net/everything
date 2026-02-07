package rmq

// PermanentError represents an error that should not be retried
// Messages causing permanent errors should be NACKed without requeue
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string {
	return e.Err.Error()
}

func (e *PermanentError) Unwrap() error {
	return e.Err
}

// IsPermanentError checks if an error is a permanent error
func IsPermanentError(err error) bool {
	_, ok := err.(*PermanentError)
	return ok
}

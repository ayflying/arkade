package account

// ValidationError distinguishes user input failures from storage/internal faults.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }
func invalid(err error) error {
	if err == nil {
		return nil
	}
	return &ValidationError{Err: err}
}

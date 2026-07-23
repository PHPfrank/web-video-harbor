package output

import "errors"

// PublishedError records that a final output path has already been atomically
// published even though later housekeeping reported an internal warning.
// Error deliberately delegates to the warning and never includes Path.
type PublishedError struct {
	path string
	err  error
}

func (e *PublishedError) Error() string         { return e.err.Error() }
func (e *PublishedError) Unwrap() error         { return e.err }
func (e *PublishedError) PublishedPath() string { return e.path }

// NewPublishedError marks path as an already-published output owned by the
// caller. Empty paths are not accepted as publication evidence.
func NewPublishedError(path string, err error) error {
	if path == "" || err == nil {
		return err
	}
	return &PublishedError{path: path, err: err}
}

// PublishedPath extracts publication evidence through wrapped errors. The
// interface keeps Engine tests and internal producers decoupled from the
// concrete marker while requiring an explicit, non-empty path.
func PublishedPath(err error) (string, bool) {
	var published interface{ PublishedPath() string }
	if !errors.As(err, &published) {
		return "", false
	}
	path := published.PublishedPath()
	return path, path != ""
}

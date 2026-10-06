package domain

import "errors"

var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrNotFound           = errors.New("resource not found")
	ErrDuplicateAssetTag  = errors.New("duplicate asset tag")
	ErrActiveCaseExists   = errors.New("device already has an unfinished case")
	ErrStateConflict      = errors.New("resource state conflict")
	ErrSealAlreadyCreated = errors.New("case already has an evidence seal")
	// ErrTimelineIntegrity means the committed case state and its event chain
	// disagree. It is a server-side corruption signal rather than a client
	// mistake and is therefore intentionally absent from IsKnownError.
	ErrTimelineIntegrity = errors.New("case timeline integrity failure")
)

func IsKnownError(err error) bool {
	return errors.Is(err, ErrInvalidInput) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrDuplicateAssetTag) ||
		errors.Is(err, ErrActiveCaseExists) ||
		errors.Is(err, ErrStateConflict) ||
		errors.Is(err, ErrSealAlreadyCreated)
}

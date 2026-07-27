package domain

import "errors"

// Domain validation errors. They describe an entity that cannot be constructed,
// independent of storage or transport, and the HTTP layer maps them to 400.
var (
	ErrEmptyEmail        = errors.New("email is required")
	ErrInvalidEmail      = errors.New("email is not a valid address")
	ErrEmptyPasswordHash = errors.New("password hash is required")

	ErrWorkerKeyNameTooLong = errors.New("worker key name is too long")
)

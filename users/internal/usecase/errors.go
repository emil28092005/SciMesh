package usecase

import "errors"

var (
	// Repository-contract errors, returned by UserRepository implementations.
	ErrEmailExists  = errors.New("email already registered")
	ErrUserNotFound = errors.New("user not found")

	// ErrWorkerKeyNotFound is returned by WorkerKeyRepository when no live key
	// matches (by id for revoke, by hash for exchange).
	ErrWorkerKeyNotFound = errors.New("worker key not found")
	// ErrInvalidWorkerKey is surfaced to the transport layer for a key that does
	// not exchange (unknown, revoked, or owner gone). Deliberately opaque so a
	// caller cannot distinguish the cases while probing.
	ErrInvalidWorkerKey = errors.New("invalid worker key")

	// Use-case errors surfaced to the transport layer.
	//
	// ErrInvalidCredentials is deliberately returned for both an unknown email
	// and a wrong password, so an attacker cannot use the response to learn
	// which emails are registered.
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrPasswordTooShort   = errors.New("password too short")
	ErrPasswordTooLong    = errors.New("password too long")
	ErrInvalidRole        = errors.New("invalid role")
)

package usecase

import "errors"

var (
	// Repository-contract errors, returned by UserRepository implementations.
	ErrEmailExists  = errors.New("email already registered")
	ErrUserNotFound = errors.New("user not found")

	// Use-case errors surfaced to the transport layer.
	//
	// ErrInvalidCredentials is deliberately returned for both an unknown email
	// and a wrong password, so an attacker cannot use the response to learn
	// which emails are registered.
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrPasswordTooShort   = errors.New("password too short")
	ErrPasswordTooLong    = errors.New("password too long")
)

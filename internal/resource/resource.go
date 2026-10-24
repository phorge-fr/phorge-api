// Package resource holds what every kind of the API has in common: the errors of a store.
package resource

import (
	"errors"
	"strings"
)

// Errors of a store.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// InUseError is a refusal to delete something that something else still uses (an image and the instances that start
// from it).
type InUseError struct{ By []string }

func (e *InUseError) Error() string { return "in use by " + strings.Join(e.By, ", ") }

// InvalidError lists what is wrong with a request, field by field.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string { return strings.Join(e.Problems, "; ") }

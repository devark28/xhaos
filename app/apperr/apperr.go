// Package apperr defines error kinds shared by every feature. Wrap them with
// fmt.Errorf("%w: detail", apperr.Invalid) and test with errors.Is.
package apperr

import "errors"

var (
	Invalid  = errors.New("invalid request")
	NotFound = errors.New("not found")
	Conflict = errors.New("conflict")
)

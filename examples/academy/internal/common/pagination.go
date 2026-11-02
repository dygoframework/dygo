// Package common holds types shared by feature modules: pagination, shared
// DTOs, guards and interceptors used in more than one module.
package common

import (
	"context"

	"github.com/dygoframework/dygo/validate"
)

// Pagination is embedded in list inputs:
//
//	type ListIn struct {
//		common.Pagination
//	}
type Pagination struct {
	Offset int `query:"offset" default:"0"`
	Limit  int `query:"limit"  default:"20"`
}

// Validate runs in the validation pipe.
func (p Pagination) Validate(context.Context) error {
	var v validate.Errors
	if p.Offset < 0 {
		v.Add("query", "offset", "must not be negative")
	}
	if p.Limit < 1 || p.Limit > 100 {
		v.Add("query", "limit", "must be between 1 and 100")
	}
	return v.Err()
}

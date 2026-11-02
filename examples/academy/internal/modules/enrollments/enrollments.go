package enrollments

import (
	"context"

	"github.com/dygoframework/dygo/validate"
)

// Enrollment is a user taking a course.
type Enrollment struct {
	ID       int `json:"id"`
	CourseID int `json:"courseId"`
	UserID   int `json:"userId"`
}

// CreateEnrollment is the body of POST /enrollments. The user is the one
// signed in.
type CreateEnrollment struct {
	CourseID int `json:"courseId" example:"1"`
}

// Validate runs in the validation pipe (validate.Methods).
func (b *CreateEnrollment) Validate(context.Context) error {
	var v validate.Errors
	if b.CourseID <= 0 {
		v.Add("body", "courseId", "is required")
	}
	return v.Err()
}

// Enrolled is emitted after a user enrolls in a course.
type Enrolled struct {
	Enrollment Enrollment
	Email      string
	Course     string
}

package courses

import (
	"context"
	"strings"

	"github.com/dygoframework/dygo/validate"
)

// Course is the stored entity and the response body.
type Course struct {
	ID        int     `json:"id"`
	Title     string  `json:"title"`
	Summary   string  `json:"summary,omitempty"`
	Level     string  `json:"level"`
	Price     float64 `json:"price"`
	Published bool    `json:"published"`
}

// CreateCourse is the body of POST /courses.
type CreateCourse struct {
	Title     string  `json:"title" example:"title 1"`
	Summary   string  `json:"summary,omitempty" example:"summary 1"`
	Level     string  `json:"level" example:"level 1"`
	Price     float64 `json:"price" example:"1.5"`
	Published bool    `json:"published" example:"true"`
}

// Validate runs in the validation pipe (validate.Methods).
func (b *CreateCourse) Validate(context.Context) error {
	var v validate.Errors
	if strings.TrimSpace(b.Title) == "" {
		v.Add("body", "title", "must not be empty")
	}
	if strings.TrimSpace(b.Level) == "" {
		v.Add("body", "level", "must not be empty")
	}
	return v.Err()
}

// Entity returns the new Course with the given ID.
func (b *CreateCourse) Entity(id int) Course {
	return Course{
		ID:        id,
		Title:     b.Title,
		Summary:   b.Summary,
		Level:     b.Level,
		Price:     b.Price,
		Published: b.Published,
	}
}

// UpdateCourse is the body of PATCH /courses/{id}. Missing fields are
// left unchanged.
type UpdateCourse struct {
	Title     *string  `json:"title,omitempty"`
	Summary   *string  `json:"summary,omitempty"`
	Level     *string  `json:"level,omitempty"`
	Price     *float64 `json:"price,omitempty"`
	Published *bool    `json:"published,omitempty"`
}

// Validate runs in the validation pipe (validate.Methods).
func (b *UpdateCourse) Validate(context.Context) error {
	var v validate.Errors
	if b.Title != nil && strings.TrimSpace(*b.Title) == "" {
		v.Add("body", "title", "must not be empty")
	}
	if b.Level != nil && strings.TrimSpace(*b.Level) == "" {
		v.Add("body", "level", "must not be empty")
	}
	return v.Err()
}

// Apply copies the fields that were sent to e.
func (b *UpdateCourse) Apply(e *Course) {
	if b.Title != nil {
		e.Title = *b.Title
	}
	if b.Summary != nil {
		e.Summary = *b.Summary
	}
	if b.Level != nil {
		e.Level = *b.Level
	}
	if b.Price != nil {
		e.Price = *b.Price
	}
	if b.Published != nil {
		e.Published = *b.Published
	}
}

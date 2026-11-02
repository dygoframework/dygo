package lessons

import (
	"context"
	"strings"

	"github.com/dygoframework/dygo/validate"
)

// Lesson is the stored entity and the response body.
type Lesson struct {
	ID       int    `json:"id"`
	CourseID int    `json:"courseID"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Position int    `json:"position"`
}

// CreateLesson is the body of POST /lessons.
type CreateLesson struct {
	CourseID int    `json:"courseID" example:"1"`
	Title    string `json:"title" example:"title 1"`
	Body     string `json:"body" example:"body 1"`
	Position int    `json:"position" example:"1"`
}

// Validate runs in the validation pipe (validate.Methods).
func (b *CreateLesson) Validate(context.Context) error {
	var v validate.Errors
	if strings.TrimSpace(b.Title) == "" {
		v.Add("body", "title", "must not be empty")
	}
	if strings.TrimSpace(b.Body) == "" {
		v.Add("body", "body", "must not be empty")
	}
	return v.Err()
}

// Entity returns the new Lesson with the given ID.
func (b *CreateLesson) Entity(id int) Lesson {
	return Lesson{
		ID:       id,
		CourseID: b.CourseID,
		Title:    b.Title,
		Body:     b.Body,
		Position: b.Position,
	}
}

// UpdateLesson is the body of PATCH /lessons/{id}. Missing fields are
// left unchanged.
type UpdateLesson struct {
	CourseID *int    `json:"courseID,omitempty"`
	Title    *string `json:"title,omitempty"`
	Body     *string `json:"body,omitempty"`
	Position *int    `json:"position,omitempty"`
}

// Validate runs in the validation pipe (validate.Methods).
func (b *UpdateLesson) Validate(context.Context) error {
	var v validate.Errors
	if b.Title != nil && strings.TrimSpace(*b.Title) == "" {
		v.Add("body", "title", "must not be empty")
	}
	if b.Body != nil && strings.TrimSpace(*b.Body) == "" {
		v.Add("body", "body", "must not be empty")
	}
	return v.Err()
}

// Apply copies the fields that were sent to e.
func (b *UpdateLesson) Apply(e *Lesson) {
	if b.CourseID != nil {
		e.CourseID = *b.CourseID
	}
	if b.Title != nil {
		e.Title = *b.Title
	}
	if b.Body != nil {
		e.Body = *b.Body
	}
	if b.Position != nil {
		e.Position = *b.Position
	}
}

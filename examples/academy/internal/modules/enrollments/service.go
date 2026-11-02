package enrollments

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/events"

	"github.com/dygoframework/dygo/examples/academy/internal/modules/courses"
)

// Service stores enrollments in memory. Swap the slice for your database.
type Service struct {
	courses *courses.Service
	events  *events.Emitter

	mu     sync.RWMutex
	items  []Enrollment
	nextID int
}

// NewService is the constructor.
func NewService(c *courses.Service, e *events.Emitter) *Service {
	return &Service{courses: c, events: e}
}

// ForUser returns the user's enrollments.
func (s *Service) ForUser(_ context.Context, userID int) []Enrollment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Enrollment{}
	for _, e := range s.items {
		if e.UserID == userID {
			out = append(out, e)
		}
	}
	return out
}

// Enroll adds the user to a published course and emits Enrolled.
func (s *Service) Enroll(ctx context.Context, userID int, email string, courseID int) (Enrollment, error) {
	c, err := s.courses.Get(ctx, courseID)
	if err != nil {
		return Enrollment{}, err // the course's 404
	}
	if !c.Published {
		return Enrollment{}, errs.NotFound(fmt.Sprintf("course %d not found", courseID))
	}
	s.mu.Lock()
	if slices.ContainsFunc(s.items, func(e Enrollment) bool { return e.UserID == userID && e.CourseID == courseID }) {
		s.mu.Unlock()
		return Enrollment{}, errs.Conflict("already enrolled", errs.Code("ALREADY_ENROLLED"))
	}
	s.nextID++
	e := Enrollment{ID: s.nextID, CourseID: courseID, UserID: userID}
	s.items = append(s.items, e)
	s.mu.Unlock()

	return e, s.events.Emit(ctx, Enrolled{Enrollment: e, Email: email, Course: c.Title})
}

// Drop removes the user's enrollment.
func (s *Service) Drop(_ context.Context, userID, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.items, func(e Enrollment) bool { return e.ID == id && e.UserID == userID })
	if i < 0 {
		return errs.NotFound(fmt.Sprintf("enrollment %d not found", id))
	}
	s.items = slices.Delete(s.items, i, i+1)
	return nil
}

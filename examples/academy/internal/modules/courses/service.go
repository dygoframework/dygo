package courses

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/dygoframework/dygo/errs"
)

// Service stores courses in memory. Swap the map for your database.
type Service struct {
	mu     sync.RWMutex
	items  map[int]Course
	nextID int
}

// NewService is the constructor.
func NewService() *Service {
	return &Service{items: map[int]Course{}}
}

// List returns every Course by ID.
func (s *Service) List(context.Context) []Course {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Collect(maps.Values(s.items))
	slices.SortFunc(out, func(a, b Course) int { return a.ID - b.ID })
	return out
}

// Get returns one Course, or a 404 error.
func (s *Service) Get(_ context.Context, id int) (Course, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	course, ok := s.items[id]
	if !ok {
		return Course{}, notFound(id)
	}
	return course, nil
}

// Create stores a new Course.
func (s *Service) Create(_ context.Context, in CreateCourse) Course {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	course := in.Entity(s.nextID)
	s.items[course.ID] = course
	return course
}

// Update changes the fields set in in.
func (s *Service) Update(_ context.Context, id int, in UpdateCourse) (Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	course, ok := s.items[id]
	if !ok {
		return Course{}, notFound(id)
	}
	in.Apply(&course)
	s.items[id] = course
	return course, nil
}

// Delete removes the Course with id.
func (s *Service) Delete(_ context.Context, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return notFound(id)
	}
	delete(s.items, id)
	return nil
}

func notFound(id int) error {
	return errs.NotFound(fmt.Sprintf("Course %d not found", id))
}

package lessons

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/dygoframework/dygo/errs"
)

// Service stores lessons in memory. Swap the map for your database.
type Service struct {
	mu     sync.RWMutex
	items  map[int]Lesson
	nextID int
}

// NewService is the constructor.
func NewService() *Service {
	return &Service{items: map[int]Lesson{}}
}

// List returns every Lesson by ID.
func (s *Service) List(context.Context) []Lesson {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Collect(maps.Values(s.items))
	slices.SortFunc(out, func(a, b Lesson) int { return a.ID - b.ID })
	return out
}

// Get returns one Lesson, or a 404 error.
func (s *Service) Get(_ context.Context, id int) (Lesson, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lesson, ok := s.items[id]
	if !ok {
		return Lesson{}, notFound(id)
	}
	return lesson, nil
}

// Create stores a new Lesson.
func (s *Service) Create(_ context.Context, in CreateLesson) Lesson {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	lesson := in.Entity(s.nextID)
	s.items[lesson.ID] = lesson
	return lesson
}

// Update changes the fields set in in.
func (s *Service) Update(_ context.Context, id int, in UpdateLesson) (Lesson, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lesson, ok := s.items[id]
	if !ok {
		return Lesson{}, notFound(id)
	}
	in.Apply(&lesson)
	s.items[id] = lesson
	return lesson, nil
}

// Delete removes the Lesson with id.
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
	return errs.NotFound(fmt.Sprintf("Lesson %d not found", id))
}

package notes

import (
	"context"
	"net/http"

	"github.com/dygoframework/dygo"
)

// Controller serves notes.
//
//dygo:controller /notes
//dygo:use openapi.Tags("notes")
type Controller struct{}

// Note is a note.
type Note struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

// IDIn is the {id} path parameter.
type IDIn struct {
	ID int `path:"id"`
}

// List answers GET /notes.
//
//dygo:get /
func (c *Controller) List(context.Context) ([]Note, error) { return nil, nil }

// Get answers GET /notes/{id}.
//
//dygo:get /{id}
//dygo:use openapi.Summary("Get a note")
//dygo:use dygo.HTTPCode(http.StatusOK)
func (c *Controller) Get(_ context.Context, in IDIn) (Note, error) { return Note{ID: in.ID}, nil }

// Export streams every note.
//
//dygo:get /export
func (c *Controller) Export(w http.ResponseWriter, _ *http.Request) error {
	_, err := w.Write([]byte("[]"))
	return err
}

// helper is not a route.
func (c *Controller) helper() {}

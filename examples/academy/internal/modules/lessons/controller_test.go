package lessons

import (
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/validate"

	"github.com/dygoframework/dygo/examples/academy/internal/modules/security"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/security/securitytest"
)

func TestCRUD(t *testing.T) {
	// Writes need the admin role, so the test boots the security module too.
	securitytest.Setup(t)
	root := &dygo.Module{Name: "test", Imports: []*dygo.Module{security.Module, Module}}
	srv := dygotest.New(root).With(validate.Use(validate.Methods())).CompileHTTP(t)

	var created Lesson
	srv.POST("/lessons").With(securitytest.Admin()).JSON(CreateLesson{
		CourseID: 1,
		Title:    "title 1",
		Body:     "body 1",
		Position: 1,
	}).Do(t).Status(t, 201).JSON(t, &created)
	if created.ID == 0 {
		t.Fatalf("created %+v", created)
	}
	srv.POST("/lessons").With(securitytest.Admin()).JSON(CreateLesson{}).Do(t).Status(t, 400)

	var list []Lesson
	srv.GET("/lessons").Do(t).Status(t, 200).JSON(t, &list)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}

	title := "changed"
	var updated Lesson
	srv.PATCH("/lessons/1").With(securitytest.Admin()).JSON(UpdateLesson{Title: &title}).Do(t).Status(t, 200).JSON(t, &updated)
	if updated.Title != title {
		t.Fatalf("updated %+v", updated)
	}

	srv.DELETE("/lessons/1").With(securitytest.Admin()).Do(t).Status(t, 204)
	srv.GET("/lessons/1").Do(t).Status(t, 404)
	srv.GET("/lessons/abc").Do(t).Status(t, 400)
}

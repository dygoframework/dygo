package courses

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

	var created Course
	srv.POST("/courses").With(securitytest.Admin()).JSON(CreateCourse{
		Title:     "title 1",
		Summary:   "summary 1",
		Level:     "level 1",
		Price:     1.5,
		Published: true,
	}).Do(t).Status(t, 201).JSON(t, &created)
	if created.ID == 0 {
		t.Fatalf("created %+v", created)
	}
	srv.POST("/courses").With(securitytest.Admin()).JSON(CreateCourse{}).Do(t).Status(t, 400)

	var list []Course
	srv.GET("/courses").Do(t).Status(t, 200).JSON(t, &list)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}

	title := "changed"
	var updated Course
	srv.PATCH("/courses/1").With(securitytest.Admin()).JSON(UpdateCourse{Title: &title}).Do(t).Status(t, 200).JSON(t, &updated)
	if updated.Title != title {
		t.Fatalf("updated %+v", updated)
	}

	srv.DELETE("/courses/1").With(securitytest.Admin()).Do(t).Status(t, 204)
	srv.GET("/courses/1").Do(t).Status(t, 404)
	srv.GET("/courses/abc").Do(t).Status(t, 400)
}

package main

import (
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"

	"github.com/dygoframework/dygo/examples/academy/internal/config"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/courses"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/enrollments"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/security"
	"github.com/dygoframework/dygo/examples/academy/internal/modules/welcomeemail"
)

func login(t *testing.T, srv *dygotest.Server, email, password string) string {
	t.Helper()
	var tok security.TokenOut
	srv.POST("/auth/login").JSON(security.Credentials{Email: email, Password: password}).
		Do(t).Status(t, 200).JSON(t, &tok)
	return "Bearer " + tok.AccessToken
}

// TestSessions shows the token lifecycle: refresh rotates the refresh token,
// a reused one ends the login, and logout ends it too.
func TestSessions(t *testing.T) {
	cfg, err := config.Load(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	srv := dygotest.New(AppModule(cfg, log)).With(AppOptions(cfg, log)...).CompileHTTP(t)
	creds := security.Credentials{Email: "bob@academy.test", Password: "bob-password"}
	srv.POST("/auth/register").JSON(creds).Do(t).Status(t, 201)
	var first, next security.TokenOut
	srv.POST("/auth/login").JSON(creds).Do(t).Status(t, 200).JSON(t, &first)

	srv.POST("/auth/refresh").JSON(security.RefreshBody{RefreshToken: first.RefreshToken}).Do(t).Status(t, 200).JSON(t, &next)
	srv.GET("/enrollments").Header("Authorization", "Bearer "+next.AccessToken).Do(t).Status(t, 200)
	srv.POST("/auth/refresh").JSON(security.RefreshBody{RefreshToken: first.RefreshToken}).Do(t).Status(t, 401) // reused: login ended
	srv.POST("/auth/refresh").JSON(security.RefreshBody{RefreshToken: next.RefreshToken}).Do(t).Status(t, 401)

	srv.POST("/auth/login").JSON(creds).Do(t).Status(t, 200).JSON(t, &first)
	srv.POST("/auth/logout").JSON(security.RefreshBody{RefreshToken: first.RefreshToken}).Do(t).Status(t, 204)
	srv.POST("/auth/refresh").JSON(security.RefreshBody{RefreshToken: first.RefreshToken}).Do(t).Status(t, 401)
}

// TestEnrollmentFlow is the main story: an admin publishes a course, a student
// registers and enrolls, and gets a welcome email from the background queue.
func TestEnrollmentFlow(t *testing.T) {
	t.Setenv("AUTH_ADMIN_EMAIL", "admin@academy.test")
	t.Setenv("AUTH_ADMIN_PASSWORD", "admin-password")
	cfg, err := config.Load(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	srv := dygotest.New(AppModule(cfg, log)).With(AppOptions(cfg, log)...).CompileHTTP(t)

	admin := login(t, srv, "admin@academy.test", "admin-password")
	course := courses.CreateCourse{Title: "Go for web developers", Level: "beginner", Price: 49, Published: true}
	srv.POST("/courses").JSON(course).Do(t).Status(t, 401)
	var c courses.Course
	srv.POST("/courses").Header("Authorization", admin).JSON(course).Do(t).Status(t, 201).JSON(t, &c)
	srv.GET("/courses").Do(t).Status(t, 200) // the catalogue is public

	srv.POST("/auth/register").JSON(map[string]string{"email": "ann@academy.test", "password": "ann-password"}).
		Do(t).Status(t, 201)
	ann := login(t, srv, "ann@academy.test", "ann-password")
	srv.POST("/courses").Header("Authorization", ann).JSON(course).Do(t).Status(t, 403) // not an admin

	var e enrollments.Enrollment
	srv.POST("/enrollments").Header("Authorization", ann).JSON(map[string]int{"courseId": c.ID}).
		Do(t).Status(t, 201).JSON(t, &e)
	srv.POST("/enrollments").Header("Authorization", ann).JSON(map[string]int{"courseId": c.ID}).
		Do(t).Status(t, 409)
	srv.POST("/enrollments").Header("Authorization", ann).JSON(map[string]int{"courseId": 99}).
		Do(t).Status(t, 404)
	srv.POST("/enrollments").Header("Authorization", ann).JSON(map[string]int{}).Do(t).Status(t, 400)

	var mine []enrollments.Enrollment
	srv.GET("/enrollments").Header("Authorization", ann).Do(t).Status(t, 200).JSON(t, &mine)
	if len(mine) != 1 || mine[0].CourseID != c.ID {
		t.Fatalf("enrollments %+v", mine)
	}
	srv.GET("/enrollments").Header("Authorization", admin).Do(t).Status(t, 200).JSON(t, &mine)
	if len(mine) != 0 {
		t.Fatalf("admin sees ann's enrollments: %+v", mine)
	}

	// The email goes out from the queue worker, not the request.
	mailer := dygo.MustGet[*welcomeemail.Processor](srv.App)
	deadline := time.Now().Add(2 * time.Second)
	for len(mailer.Sent()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no welcome email")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := mailer.Sent()[0]; got.Email != "ann@academy.test" || got.Course != course.Title {
		t.Fatalf("sent %+v", got)
	}

	srv.DELETE("/enrollments/"+strconv.Itoa(e.ID)).Header("Authorization", admin).Do(t).Status(t, 404) // not admin's
	srv.DELETE("/enrollments/"+strconv.Itoa(e.ID)).Header("Authorization", ann).Do(t).Status(t, 204)
}

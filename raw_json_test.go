package dygo_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/dygoframework/dygo"
)

type rawJSONController struct{}

func (rawJSONController) Routes(r dygo.Router) {
	r.Post("/raw", dygo.HandleNoInput(func(context.Context) (dygo.RawJSON, error) {
		return dygo.RawJSON(`{"ok":true}`), nil
	}), dygo.HTTPCode(202), dygo.Header("X-Kind", "raw"))
}

func TestRawJSONIsWrittenAsIs(t *testing.T) {
	app, err := dygo.Create(context.Background(), &dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(rawJSONController{})}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/raw", nil))
	if w.Code != 202 || w.Body.String() != "{\"ok\":true}\n" ||
		w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Header().Get("X-Kind") != "raw" {
		t.Errorf("%d %q %v", w.Code, w.Body.String(), w.Header())
	}
}

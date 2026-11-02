package micro_test

import (
	"context"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/micro"
)

type benchH struct{}

func (benchH) Patterns(r micro.Router) {
	r.Message("users.get", micro.Handle(func(_ context.Context, in GetUser) (*User, error) {
		return &User{ID: in.ID, Name: "Ada Lovelace, first programmer"}, nil
	}))
}

func BenchmarkLocalSend(b *testing.B) {
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name: "u", Providers: []dygo.Provider{dygo.Value(benchH{})},
	})
	if err != nil {
		b.Fatal(err)
	}
	local := micro.NewLocal(micro.ServerOptions{})
	app.ConnectMicroservice(local)
	if err := app.StartAllMicroservices(context.Background()); err != nil {
		b.Fatal(err)
	}
	c := micro.NewClient(local)
	ctx, cancel := context.WithCancel(context.Background()) // has no deadline -> timeout path
	defer cancel()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := micro.Send[*User](ctx, c, "users.get", GetUser{ID: "1"}); err != nil {
			b.Fatal(err)
		}
	}
}

var big = func() []User {
	us := make([]User, 200)
	for i := range us {
		us[i] = User{ID: "12345", Name: "Ada Lovelace, first programmer"}
	}
	return us
}()

func BenchmarkEncodeReply(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = micro.EncodeReply(big, nil)
	}
}

var bigReply = micro.EncodeReply(big, nil)

func BenchmarkDecodeReply(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var out []User
		if err := micro.DecodeReply(bigReply, &out); err != nil {
			b.Fatal(err)
		}
	}
}

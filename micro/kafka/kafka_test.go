package kafka_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/micro"
	dygokafka "github.com/dygoframework/dygo/micro/kafka"
	"github.com/twmb/franz-go/pkg/kfake"
)

type Add struct{ A, B int }

type math struct{ got chan string }

func (m *math) Patterns(r micro.Router) {
	r.Message("math.add", micro.Handle(func(ctx context.Context, in Add) (int, error) {
		if in.A < 0 {
			return 0, errs.BadRequest("negative")
		}
		return in.A + in.B, nil
	}))
	r.Event(`^audit\..*`, micro.HandleEvent(func(ctx context.Context, _ Add) error {
		msg, _ := micro.MessageFrom(ctx)
		m.got <- msg.Pattern + " " + msg.Header["source"]
		return nil
	}))
}

func TestServiceOverKafka(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation(), kfake.SeedTopics(1, "math.add", "audit.users"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	brokers := cluster.ListenAddrs()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := &math{got: make(chan string, 2)}
	svc, err := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "math", Providers: []dygo.Provider{dygo.Value(m)}})
	if err != nil {
		t.Fatal(err)
	}
	svc.ConnectMicroservice(dygokafka.NewServer(dygokafka.Options{Brokers: brokers, GroupID: "math", FromBeginning: true}))
	if err := svc.StartAllMicroservices(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Close(context.Background())

	gw, err := dygo.CreateApplicationContext(ctx, &dygo.Module{
		Name:    "gateway",
		Imports: []*dygo.Module{dygokafka.ClientModule(dygokafka.ClientOptions{Brokers: brokers})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close(context.Background())
	c := dygo.MustGet[*micro.Client](gw)

	if sum, err := micro.Send[int](ctx, c, "math.add", Add{A: 2, B: 3}); err != nil || sum != 5 {
		t.Fatalf("add = %d, %v", sum, err)
	}
	var he *errs.HTTPError
	if _, err := micro.Send[int](ctx, c, "math.add", Add{A: -1}); !errors.As(err, &he) || he.Status != 400 {
		t.Errorf("err = %v", err)
	}
	if err := c.Emit(micro.WithHeader(ctx, "source", "users"), "audit.users", Add{}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-m.got:
		if got != "audit.users users" {
			t.Errorf("event = %q", got)
		}
	case <-ctx.Done():
		t.Fatal("event not delivered")
	}
}

func TestBadRegexpFailsListen(t *testing.T) {
	ctx := context.Background()
	app, err := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Value(bad{})}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	app.ConnectMicroservice(dygokafka.NewServer(dygokafka.Options{Brokers: []string{"127.0.0.1:1"}, GroupID: "g"}))
	if err := app.StartAllMicroservices(ctx); err == nil {
		t.Fatal("want an error for a bad regexp")
	}
}

type bad struct{}

func (bad) Patterns(r micro.Router) {
	r.Event("^audit.(", micro.HandleEvent(func(context.Context, Add) error { return nil }))
}

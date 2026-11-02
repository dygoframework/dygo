package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/micro"
	dygokafka "github.com/dygoframework/dygo/micro/kafka"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func benchKafka(b *testing.B, opts ...kgo.Opt) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation(), kfake.SeedTopics(1, "math.add", "audit.users"))
	if err != nil {
		b.Fatal(err)
	}
	defer cluster.Close()
	brokers := cluster.ListenAddrs()
	ctx := context.Background()
	m := &math{got: make(chan string, 1<<20)}
	svc, _ := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "math", Providers: []dygo.Provider{dygo.Value(m)}})
	svc.ConnectMicroservice(dygokafka.NewServer(dygokafka.Options{Brokers: brokers, GroupID: "math", FromBeginning: true, Kafka: opts}))
	if err := svc.StartAllMicroservices(ctx); err != nil {
		b.Fatal(err)
	}
	defer svc.Close(ctx)
	t, err := dygokafka.Dial(ctx, brokers, opts...)
	if err != nil {
		b.Fatal(err)
	}
	defer t.Close(ctx)
	c := micro.NewClient(t, micro.Timeout(20*time.Second))
	if _, err := micro.Send[int](ctx, c, "math.add", Add{A: 1}); err != nil { // warm up
		b.Fatal(err)
	}
	b.Run("Send", func(b *testing.B) {
		for b.Loop() {
			if _, err := micro.Send[int](ctx, c, "math.add", Add{A: 2, B: 3}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Emit", func(b *testing.B) {
		for b.Loop() {
			if err := c.Emit(ctx, "audit.users", Add{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkKafka(b *testing.B) { benchKafka(b) }

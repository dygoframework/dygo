// Command orders is the phase 6 example: one HTTP route creates an order
// through the command bus, the order service emits an event, a listener puts
// a confirmation email on the queue, and a scheduled job reports the count.
// GET /orders/{id} goes through the query bus and the response cache. The
// same handlers answer microservice messages (orders.get) over an
// in-process transport; swap in micro/nats to serve them over NATS.
//
//	go run ./examples/orders
//	curl -X POST localhost:9900/orders -d '{"item":"pen","qty":2}'
//	curl localhost:9900/orders/1
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/cache"
	"github.com/dygoframework/dygo/cqrs"
	"github.com/dygoframework/dygo/events"
	"github.com/dygoframework/dygo/micro"
	"github.com/dygoframework/dygo/queue"
	"github.com/dygoframework/dygo/schedule"
)

// AppModule wires the phase 6 modules and the orders feature.
func AppModule() *dygo.Module {
	return &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{
			events.Module(),
			schedule.Module(),
			cqrs.Module(),
			cache.Module(cache.Options{TTL: time.Minute}),
			queue.Module(queue.Options{}),
			OrdersModule,
		},
	}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(log)
	ctx := context.Background()
	app, err := dygo.Create(ctx, AppModule(), dygo.WithLogger(log))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app.ConnectMicroservice(micro.NewLocal(micro.ServerOptions{}))
	if err := app.Listen(ctx, ":9900"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

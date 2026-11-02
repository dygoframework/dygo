package main

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/cache"
	"github.com/dygoframework/dygo/cqrs"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/events"
	"github.com/dygoframework/dygo/micro"
	"github.com/dygoframework/dygo/queue"
	"github.com/dygoframework/dygo/schedule"
)

// OrdersModule is the orders feature.
var OrdersModule = &dygo.Module{
	Name:        "orders",
	Providers:   []dygo.Provider{dygo.Provide(NewStore), dygo.Provide(NewHandlers), dygo.Provide(NewMailer), dygo.Provide(NewReport)},
	Controllers: []dygo.Provider{dygo.Provide(NewController)},
}

// Order is the entity.
type Order struct {
	ID   string `json:"id"`
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

// PlaceOrder is a command; OrderByID a query; OrderPlaced an event.
type (
	PlaceOrder struct {
		Item string
		Qty  int
		ID   *string // set by the handler
	}
	OrderByID   struct{ ID string }
	OrderPlaced struct{ Order Order }
)

// Store keeps orders in memory.
type Store struct {
	mu     sync.Mutex
	orders map[string]Order
}

// NewStore is the constructor.
func NewStore() *Store { return &Store{orders: map[string]Order{}} }

// Count returns the number of orders.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.orders)
}

// Handlers are the command and query handlers, and the microservice
// patterns.
type Handlers struct {
	store  *Store
	events *events.Emitter
	query  *cqrs.QueryBus
}

// NewHandlers is the constructor.
func NewHandlers(s *Store, em *events.Emitter, q *cqrs.QueryBus) *Handlers {
	return &Handlers{store: s, events: em, query: q}
}

// CQRS registers the bus handlers.
func (h *Handlers) CQRS(r *cqrs.Registry) {
	cqrs.HandleCommand(r, h.place)
	cqrs.HandleQuery(r, h.byID)
}

// Patterns answers orders.get from other services.
func (h *Handlers) Patterns(r micro.Router) {
	r.Message("orders.get", micro.Handle(func(ctx context.Context, q OrderByID) (Order, error) {
		return cqrs.Ask[Order](ctx, h.query, q)
	}))
}

func (h *Handlers) place(ctx context.Context, c PlaceOrder) error {
	if c.Item == "" || c.Qty <= 0 {
		return errs.BadRequest("item and a positive qty are required")
	}
	h.store.mu.Lock()
	o := Order{ID: strconv.Itoa(len(h.store.orders) + 1), Item: c.Item, Qty: c.Qty}
	h.store.orders[o.ID] = o
	h.store.mu.Unlock()
	if c.ID != nil {
		*c.ID = o.ID
	}
	return h.events.Emit(ctx, OrderPlaced{Order: o})
}

func (h *Handlers) byID(_ context.Context, q OrderByID) (Order, error) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	o, ok := h.store.orders[q.ID]
	if !ok {
		return Order{}, errs.NotFound("order " + q.ID + " not found")
	}
	return o, nil
}

// Mailer listens for placed orders and sends the confirmation from the
// queue.
type Mailer struct {
	queue *queue.Queue
	mu    sync.Mutex
	Sent  []string
}

// NewMailer is the constructor.
func NewMailer(q *queue.Queue) *Mailer { return &Mailer{queue: q} }

// Events subscribes to OrderPlaced.
func (m *Mailer) Events(r *events.Registry) {
	events.On(r, func(ctx context.Context, e OrderPlaced) error {
		_, err := m.queue.Add(ctx, "order-confirmation", e.Order)
		return err
	})
}

// Process sends confirmations.
func (m *Mailer) Process(r *queue.Registry) {
	queue.Handle(r, "order-confirmation", func(ctx context.Context, o Order) error {
		m.mu.Lock()
		m.Sent = append(m.Sent, o.ID)
		m.mu.Unlock()
		slog.InfoContext(ctx, "confirmation sent", "order", o.ID)
		return nil
	})
}

// Report logs the order count every minute.
type Report struct{ store *Store }

// NewReport is the constructor.
func NewReport(s *Store) *Report { return &Report{store: s} }

// Jobs declares the schedule.
func (r *Report) Jobs(s *schedule.Registry) {
	s.Every("order-report", time.Minute, func(ctx context.Context) error {
		slog.InfoContext(ctx, "orders so far", "count", r.store.Count())
		return nil
	})
}

// Controller is the HTTP side.
type Controller struct {
	commands *cqrs.CommandBus
	queries  *cqrs.QueryBus
}

// NewController is the constructor.
func NewController(c *cqrs.CommandBus, q *cqrs.QueryBus) *Controller {
	return &Controller{commands: c, queries: q}
}

// CreateIn is the POST body.
type CreateIn struct {
	Body struct {
		Item string `json:"item"`
		Qty  int    `json:"qty"`
	} `body:"json"`
}

// GetIn is the path of GET /orders/{id}.
type GetIn struct {
	ID string `path:"id"`
}

// Routes registers the routes; GET responses are cached.
func (c *Controller) Routes(r dygo.Router) {
	r = r.Prefix("/orders")
	r.Post("", dygo.Handle(c.create))
	r.Get("/{id}", dygo.Handle(c.get), dygo.Interceptors(dygo.Ref[*cache.Interceptor]()), cache.TTL(30*time.Second))
}

func (c *Controller) create(ctx context.Context, in CreateIn) (map[string]string, error) {
	var id string
	err := c.commands.Execute(ctx, PlaceOrder{Item: in.Body.Item, Qty: in.Body.Qty, ID: &id})
	return map[string]string{"id": id}, err
}

func (c *Controller) get(ctx context.Context, in GetIn) (Order, error) {
	return cqrs.Ask[Order](ctx, c.queries, OrderByID{ID: in.ID})
}

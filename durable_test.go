package dygo_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dygoframework/dygo"
)

// TenantDB is durable: one per tenant, shared by that tenant's requests.
type TenantDB struct {
	Tenant string
	closed atomic.Bool
}

// TenantCache is durable too and depends on TenantDB.
type TenantCache struct{ DB *TenantDB }

// PerRequest is a plain request scoped provider.
type PerRequest struct{ N int64 }

type durableCounts struct {
	dbs, caches, requests atomic.Int64
}

func (db *TenantDB) OnApplicationShutdown(context.Context, string) error {
	db.closed.Store(true)
	return nil
}

type tenantController struct {
	cache dygo.Scoped[*TenantCache]
	req   dygo.Scoped[*PerRequest]
}

func (c *tenantController) Routes(r dygo.Router) {
	r.Get("/whoami", dygo.HandleNoInput(func(ctx context.Context) (string, error) {
		tc, err := c.cache.Get(ctx)
		if err != nil {
			return "", err
		}
		pr, err := c.req.Get(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %p %d", tc.DB.Tenant, tc.DB, pr.N), nil
	}))
}

func tenantModule(n *durableCounts) *dygo.Module {
	return &dygo.Module{
		Name: "app",
		Providers: []dygo.Provider{
			dygo.Provide(func(cid dygo.ContextID) (*TenantDB, error) {
				n.dbs.Add(1)
				if cid.ID == "bad" {
					return nil, errors.New("unknown tenant")
				}
				return &TenantDB{Tenant: cid.Payload.(string)}, nil
			}, dygo.WithScope(dygo.RequestScoped), dygo.Durable()),
			dygo.Provide(func(db *TenantDB) *TenantCache {
				n.caches.Add(1)
				return &TenantCache{DB: db}
			}, dygo.WithScope(dygo.RequestScoped), dygo.Durable()),
			dygo.Provide(func() *PerRequest { return &PerRequest{N: n.requests.Add(1)} }, dygo.WithScope(dygo.RequestScoped)),
		},
		Controllers: []dygo.Provider{dygo.Provide(func(c dygo.Scoped[*TenantCache], r dygo.Scoped[*PerRequest]) *tenantController {
			return &tenantController{cache: c, req: r}
		})},
	}
}

func tenantStrategy(r *http.Request) (string, any) {
	t := r.Header.Get("X-Tenant")
	return t, "tenant-" + t
}

func getTenant(t *testing.T, url, tenant string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/whoami", nil)
	if tenant != "" {
		req.Header.Set("X-Tenant", tenant)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestDurableProvidersPerTenant(t *testing.T) {
	n := &durableCounts{}
	app, err := dygo.Create(context.Background(), tenantModule(n), dygo.WithContextIDStrategy(tenantStrategy))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	ptrs := map[string]string{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range 20 {
		tenant := []string{"a", "b"}[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body := getTenant(t, srv.URL, tenant)
			if code != 200 {
				t.Errorf("%s: %d %s", tenant, code, body)
				return
			}
			f := strings.Fields(body)
			mu.Lock()
			defer mu.Unlock()
			if f[0] != "tenant-"+tenant {
				t.Errorf("tenant %s got %s", tenant, f[0])
			}
			if p, ok := ptrs[tenant]; ok && p != f[1] {
				t.Errorf("tenant %s got two instances", tenant)
			}
			ptrs[tenant] = f[1]
		}()
	}
	wg.Wait()
	if n.dbs.Load() != 2 || n.caches.Load() != 2 {
		t.Errorf("built %d dbs and %d caches, want 2 each (one per tenant, even when requests race)", n.dbs.Load(), n.caches.Load())
	}
	if n.requests.Load() != 20 {
		t.Errorf("per-request provider built %d times, want 20", n.requests.Load())
	}
	ids := app.DurableContexts()
	slices.Sort(ids)
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("contexts = %v", ids)
	}

	// No tenant: durable providers act per request.
	before := n.dbs.Load()
	getTenant(t, srv.URL, "")
	getTenant(t, srv.URL, "")
	if n.dbs.Load()-before != 2 {
		t.Errorf("without an ID, built %d dbs for 2 requests", n.dbs.Load()-before)
	}

	// A failing constructor is retried by the next request.
	if code, _ := getTenant(t, srv.URL, "bad"); code != 500 {
		t.Errorf("bad tenant = %d", code)
	}
	before = n.dbs.Load()
	getTenant(t, srv.URL, "bad")
	if n.dbs.Load() != before+1 {
		t.Error("failed durable construction was cached")
	}

	// Outside a request, WithDurableContext reaches the same instances.
	ctrl := app.Controllers()[0].(*tenantController)
	tc, err := ctrl.cache.Get(dygo.WithDurableContext(context.Background(), "a", "ignored"))
	if err != nil || fmt.Sprintf("%p", tc.DB) != ptrs["a"] {
		t.Errorf("WithDurableContext = %v, %v", tc, err)
	}
	if _, err := ctrl.req.Get(dygo.WithDurableContext(context.Background(), "a", nil)); err == nil || !strings.Contains(err.Error(), "not durable") {
		t.Errorf("per-request provider outside a request: %v", err)
	}

	// Releasing a tenant shuts its instances down; the next request rebuilds.
	dbA := tc.DB
	if err := app.ReleaseContext(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if !dbA.closed.Load() {
		t.Error("released TenantDB was not shut down")
	}
	before = n.dbs.Load()
	getTenant(t, srv.URL, "a")
	if n.dbs.Load() != before+1 {
		t.Error("tenant a was not rebuilt after release")
	}

	tcB, _ := ctrl.cache.Get(dygo.WithDurableContext(context.Background(), "b", nil))
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !tcB.DB.closed.Load() {
		t.Error("shutdown did not reach durable instances")
	}
}

func TestDurableBootErrors(t *testing.T) {
	type Plain struct{}
	type Bad struct{}
	cases := []struct {
		name string
		p    []dygo.Provider
		want string
	}{
		{"not request scoped", []dygo.Provider{dygo.Provide(func() *Bad { return nil }, dygo.Durable())}, "durable needs dygo.WithScope(dygo.RequestScoped)"},
		{"injects the request", []dygo.Provider{
			dygo.Provide(func(*http.Request) *Bad { return nil }, dygo.WithScope(dygo.RequestScoped), dygo.Durable()),
		}, "injects *http.Request, but its instance is shared"},
		{"injects a per-request provider", []dygo.Provider{
			dygo.Provide(func() *Plain { return nil }, dygo.WithScope(dygo.RequestScoped)),
			dygo.Provide(func(*Plain) *Bad { return nil }, dygo.WithScope(dygo.RequestScoped), dygo.Durable()),
		}, "request scoped but not durable"},
		{"singleton injects ContextID", []dygo.Provider{dygo.Provide(func(dygo.ContextID) *Bad { return nil })}, "injects dygo.ContextID"},
	}
	for _, c := range cases {
		_, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{Name: "app", Providers: c.p})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

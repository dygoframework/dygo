package dygo_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/dygoframework/dygo"
)

type scopes struct {
	cache dygo.Scoped[*TenantCache]
	req   dygo.Scoped[*PerRequest]
}

func TestWithRequestScope(t *testing.T) {
	n := &durableCounts{}
	m := tenantModule(n)
	m.Providers = append(m.Providers, dygo.Provide(func(c dygo.Scoped[*TenantCache], r dygo.Scoped[*PerRequest]) *scopes {
		return &scopes{cache: c, req: r}
	}))
	app, err := dygo.CreateApplicationContext(context.Background(), m, dygo.WithContextIDStrategy(tenantStrategy))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	ctrl := dygo.MustGet[*scopes](app)
	hs := httptest.NewRequest("GET", "/ws", nil)
	hs.Header.Set("X-Tenant", "acme")

	get := func(ctx context.Context) (*TenantCache, *PerRequest) {
		t.Helper()
		tc, err := ctrl.cache.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		pr, err := ctrl.req.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return tc, pr
	}
	ctx1, end1 := app.WithRequestScope(context.Background(), hs)
	tc1, pr1 := get(ctx1)
	if again, _ := ctrl.req.Get(ctx1); again != pr1 {
		t.Error("a scope builds a request scoped provider once")
	}
	end1()
	ctx2, end2 := app.WithRequestScope(context.Background(), hs)
	defer end2()
	tc2, pr2 := get(ctx2)
	if pr1 == pr2 {
		t.Error("two scopes share a request scoped instance")
	}
	if tc1 != tc2 || tc1.DB.Tenant != "tenant-acme" {
		t.Errorf("durable instances differ or have the wrong tenant: %p %p %q", tc1, tc2, tc1.DB.Tenant)
	}
	if _, err := ctrl.req.Get(ctx1); err == nil {
		t.Error("an ended scope still resolves")
	}
	if n.requests.Load() != 2 {
		t.Errorf("built %d request scoped instances, want 2", n.requests.Load())
	}
}

package migrate

import (
	"context"
	"io/fs"
	"log/slog"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/sql"
)

// Applied is what [OnBoot] provides: the migrations it applied at boot.
type Applied []Migration

// OnBoot returns a module that applies the pending migrations in fsys while
// the app boots, before it serves traffic. Use
// it for a small single instance service; with several instances, run
// db:migrate as a deploy step instead, so a failing migration stops the
// deploy rather than every instance. The lock still keeps two instances from
// migrating at once.
//
//	Imports: []*dygo.Module{postgres.Module(), migrate.OnBoot(migrations.FS)}
//
// Only providers that inject [Applied] are sure to be built after the
// migrations ran; others that query the database in their constructor should
// inject it too.
func OnBoot(fsys fs.FS, opts ...Option) *dygo.Module {
	return &dygo.Module{
		Name:   "sql.migrate",
		Global: true,
		Providers: []dygo.Provider{dygo.Provide(func(ctx context.Context, db *sql.DB, logger dygo.Optional[*slog.Logger]) (Applied, error) {
			log := logger.OrElse(slog.Default())
			m, err := New(db, fsys, opts...)
			if err != nil {
				return nil, err
			}
			done, err := m.Up(ctx, 0)
			for _, mig := range done {
				log.Info("migrate: applied", "migration", mig.String())
			}
			return Applied(done), err
		})},
		Exports: []dygo.Token{dygo.TypeOf[Applied]()},
	}
}

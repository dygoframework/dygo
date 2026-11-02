module github.com/dygoframework/dygo/micro/redis

go 1.27.0

replace github.com/dygoframework/dygo => ../..

require (
	github.com/dygoframework/dygo v1.0.0-beta.1
	github.com/redis/go-redis/v9 v9.23.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

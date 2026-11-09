module github.com/dygoframework/dygo/examples/chat

go 1.27.0

require (
	github.com/dygoframework/dygo v1.0.0-beta.2
	github.com/dygoframework/dygo/ai v0.0.0-00010101000000-000000000000
)

replace github.com/dygoframework/dygo => ../..

replace github.com/dygoframework/dygo/ai => ../../ai

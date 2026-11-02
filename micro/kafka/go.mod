module github.com/dygoframework/dygo/micro/kafka

go 1.27.0

replace github.com/dygoframework/dygo => ../..

require (
	github.com/dygoframework/dygo v1.0.0-beta.1
	github.com/twmb/franz-go v1.22.2
	github.com/twmb/franz-go/pkg/kadm v1.19.0
	github.com/twmb/franz-go/pkg/kfake v0.0.0-20260218082530-ae75cacb982c
)

require (
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.15.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
)

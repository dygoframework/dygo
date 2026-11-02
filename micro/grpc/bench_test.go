package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"
)

func BenchmarkFromIncomingContext(b *testing.B) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		":authority", "localhost", "content-type", "application/grpc", "user-agent", "grpc-go/1.80",
		"authorization", "Bearer abc.def.ghi", "x-request-id", "0123456789abcdef"))
	b.ReportAllocs()
	for b.Loop() {
		md, _ := metadata.FromIncomingContext(ctx)
		_ = &Call{FullMethod: "/x.Y/Z", Metadata: md}
	}
}

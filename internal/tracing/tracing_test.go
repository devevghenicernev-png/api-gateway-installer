package tracing

import (
	"context"
	"testing"
)

func TestNoEndpointIsNoOp(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{})
	if err != nil {
		t.Fatalf("expected no-op success; got %v", err)
	}
	if shutdown == nil {
		t.Fatalf("shutdown must be non-nil even on no-op")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("no-op shutdown should not error; got %v", err)
	}
}

func TestTracerReturnsUsableTracer(t *testing.T) {
	tr := Tracer("test")
	if tr == nil {
		t.Fatal("Tracer returned nil")
	}
	ctx, span := tr.Start(context.Background(), "test-span")
	if span == nil {
		t.Fatal("Start returned nil span")
	}
	if ctx == nil {
		t.Fatal("Start returned nil ctx")
	}
	span.End()
}

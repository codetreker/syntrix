package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MockServerStream mocks grpc.ServerStream
type MockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *MockServerStream) Context() context.Context {
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}

func (m *MockServerStream) RecvMsg(m2 interface{}) error {
	return nil
}

func (m *MockServerStream) SendMsg(m2 interface{}) error {
	return nil
}

func TestUnaryInterceptors(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	info := &grpc.UnaryServerInfo{
		FullMethod: "/test.Service/Method",
	}

	t.Run("Recovery Success", func(t *testing.T) {
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "response", nil
		}
		resp, err := srv.recoveryUnaryInterceptor(context.Background(), "request", info, handler)
		assert.NoError(t, err)
		assert.Equal(t, "response", resp)
	})

	t.Run("Recovery Panic", func(t *testing.T) {
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			panic("oops")
		}
		_, err := srv.recoveryUnaryInterceptor(context.Background(), "request", info, handler)
		assert.Error(t, err)
		st, ok := status.FromError(err)
		assert.True(t, ok)
		assert.Equal(t, codes.Internal, st.Code())
	})

	t.Run("Logging Success", func(t *testing.T) {
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "response", nil
		}
		resp, err := srv.loggingUnaryInterceptor(context.Background(), "request", info, handler)
		assert.NoError(t, err)
		assert.Equal(t, "response", resp)
	})

	t.Run("Logging Error", func(t *testing.T) {
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, status.Error(codes.InvalidArgument, "bad request")
		}
		_, err := srv.loggingUnaryInterceptor(context.Background(), "request", info, handler)
		assert.Error(t, err)
	})

	t.Run("Logging Canceled", func(t *testing.T) {
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, status.Error(codes.Canceled, "canceled")
		}
		_, err := srv.loggingUnaryInterceptor(context.Background(), "request", info, handler)
		assert.Error(t, err)
	})

	t.Run("Logging DeadlineExceeded", func(t *testing.T) {
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, status.Error(codes.DeadlineExceeded, "deadline exceeded")
		}
		_, err := srv.loggingUnaryInterceptor(context.Background(), "request", info, handler)
		assert.Error(t, err)
	})

	t.Run("Logging NotFound", func(t *testing.T) {
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, status.Error(codes.NotFound, "not found")
		}
		_, err := srv.loggingUnaryInterceptor(context.Background(), "request", info, handler)
		assert.Error(t, err)
	})

	t.Run("Logging Internal with context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, status.Error(codes.Internal, "internal error")
		}
		_, err := srv.loggingUnaryInterceptor(ctx, "request", info, handler)
		assert.Error(t, err)
	})

	t.Run("Logging Internal without context error", func(t *testing.T) {
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, status.Error(codes.Internal, "internal error")
		}
		_, err := srv.loggingUnaryInterceptor(context.Background(), "request", info, handler)
		assert.Error(t, err)
	})
}

func TestStreamInterceptors(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	info := &grpc.StreamServerInfo{
		FullMethod: "/test.Service/Stream",
	}
	mockStream := &MockServerStream{ctx: context.Background()}

	t.Run("Recovery Success", func(t *testing.T) {
		handler := func(srv interface{}, stream grpc.ServerStream) error {
			return nil
		}
		err := srv.recoveryStreamInterceptor(nil, mockStream, info, handler)
		assert.NoError(t, err)
	})

	t.Run("Recovery Panic", func(t *testing.T) {
		handler := func(srv interface{}, stream grpc.ServerStream) error {
			panic("oops")
		}
		err := srv.recoveryStreamInterceptor(nil, mockStream, info, handler)
		assert.Error(t, err)
		st, ok := status.FromError(err)
		assert.True(t, ok)
		assert.Equal(t, codes.Internal, st.Code())
	})

	t.Run("Logging Success", func(t *testing.T) {
		handler := func(srv interface{}, stream grpc.ServerStream) error {
			return nil
		}
		err := srv.loggingStreamInterceptor(nil, mockStream, info, handler)
		assert.NoError(t, err)
	})

	t.Run("Logging Error", func(t *testing.T) {
		handler := func(srv interface{}, stream grpc.ServerStream) error {
			return status.Error(codes.InvalidArgument, "bad request")
		}
		err := srv.loggingStreamInterceptor(nil, mockStream, info, handler)
		assert.Error(t, err)
	})
}

func TestRegisterGRPCService(t *testing.T) {
	srv := New(Config{}, nil)

	desc := &grpc.ServiceDesc{
		ServiceName: "test.Service",
		HandlerType: (*interface{})(nil),
		Methods:     []grpc.MethodDesc{},
		Streams:     []grpc.StreamDesc{},
		Metadata:    "test.proto",
	}

	// Should not panic
	srv.RegisterGRPCService(desc, nil)
}

func TestRegisterGRPC(t *testing.T) {
	InitDefault(Config{}, nil)
	defer func() { SetDefault(nil) }()

	desc := &grpc.ServiceDesc{
		ServiceName: "test.Service",
		HandlerType: (*interface{})(nil),
		Methods:     []grpc.MethodDesc{},
		Streams:     []grpc.StreamDesc{},
		Metadata:    "test.proto",
	}

	// Should not panic
	RegisterGRPC(desc, nil)
}

// Mock for metadata test
type mockStreamWithContext struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockStreamWithContext) Context() context.Context {
	return m.ctx
}

func TestStreamInterceptors_Context(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)
	info := &grpc.StreamServerInfo{FullMethod: "/test"}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	stream := &mockStreamWithContext{ctx: ctx}

	handler := func(srv interface{}, stream grpc.ServerStream) error {
		return nil
	}

	err := srv.loggingStreamInterceptor(nil, stream, info, handler)
	assert.NoError(t, err)
}

package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// maxTokenFileBytes bounds what is read from a projected token file.
const maxTokenFileBytes = 16 << 10

// TokenFile is a token that kubelet keeps up to date in a projected volume
// (ADR-0013). It is read on every use, so a renewed token is picked up
// without a restart. The token itself is never logged.
type TokenFile string

// Token returns the current token.
func (f TokenFile) Token() (string, error) {
	if f == "" {
		return "", errors.New("token dosyası tanımlı değil")
	}
	file, err := os.Open(string(f))
	if err != nil {
		return "", fmt.Errorf("token dosyası açılamadı: %w", err)
	}
	defer func() { _ = file.Close() }()
	b, err := io.ReadAll(io.LimitReader(file, maxTokenFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("token dosyası okunamadı: %w", err)
	}
	if len(b) > maxTokenFileBytes {
		return "", errors.New("token dosyası çok büyük")
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("token dosyası boş")
	}
	return tok, nil
}

// ServiceTokenFrom returns client options that attach the service token from
// f to every call. A connection uses it only for one target service: the
// token's audience is that service, so it is useless anywhere else.
//
// A call whose context already carries an authorization header (a user's
// token) fails instead of sending two identities.
func ServiceTokenFrom(f TokenFile) []grpc.DialOption {
	attach := func(ctx context.Context) (context.Context, error) {
		if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get(authHeader)) > 0 {
			return nil, errors.New("servis çağrısında kullanıcı token'ı bulunamaz")
		}
		tok, err := f.Token()
		if err != nil {
			return nil, err
		}
		return WithToken(ctx, tok), nil
	}
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			ctx, err := attach(ctx)
			if err != nil {
				return err
			}
			return invoker(ctx, method, req, reply, cc, opts...)
		}),
		grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			ctx, err := attach(ctx)
			if err != nil {
				return nil, err
			}
			return streamer(ctx, desc, cc, method, opts...)
		}),
	}
}

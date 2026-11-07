package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// HTTPCheck returns a Check that succeeds when GET url answers 200.
func HTTPCheck(client *http.Client, url string) Check {
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("istek oluşturulamadı: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("erişilemiyor: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("hazır değil: HTTP %d", resp.StatusCode)
		}
		return nil
	}
}

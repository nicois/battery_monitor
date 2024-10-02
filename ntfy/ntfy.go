// /usr/bin/true; exec /usr/bin/env go run "$0" "$@"
package ntfy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

type Message struct {
	Text    string
	Headers map[string]string
}

type ntfy struct {
	url string
}

func (n *ntfy) Send(ctx context.Context, logger *zap.Logger, message Message) error {
	logger.Info("Sending to NTFY", zap.String("message", message.Text), zap.String("headers", fmt.Sprintf("%+v", message.Headers)))
	req, _ := http.NewRequestWithContext(ctx, "POST", n.url, strings.NewReader(message.Text))
	for k, v := range message.Headers {
		req.Header.Set(k, v)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			if body, err := io.ReadAll(resp.Body); err == nil {
				logger.Info("Got response from ntfy server", zap.Int("response", resp.StatusCode), zap.String("body", string(body)))
			} else {
				return fmt.Errorf("While reading response from ntfy: %w", err)
			}
		}
	} else {
		return fmt.Errorf("While trying to write %v to %v: %w", message.Headers, n.url, err)
	}
	return nil
}

func Create(topic string) *ntfy {
	return &ntfy{url: "https://ntfy.sh/" + topic}
}

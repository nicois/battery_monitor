// /usr/bin/true; exec /usr/bin/env go run "$0" "$@"
package ntfy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

type Message struct {
	Text    string
	Headers map[string]string
}

type ntfy struct {
	url string
}

func (n *ntfy) Send(ctx context.Context, message Message) error {
	log.Printf("Sending message %q with headers %v\n", message.Text, message.Headers)
	req, _ := http.NewRequestWithContext(ctx, "POST", n.url, strings.NewReader(message.Text))
	for k, v := range message.Headers {
		req.Header.Set(k, v)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			if body, err := io.ReadAll(resp.Body); err == nil {
				log.Printf("Got response from ntfy server: %v: %v\n", resp, string(body))
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

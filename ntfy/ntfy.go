///usr/bin/true; exec /usr/bin/env go run "$0" "$@"
package ntfy

import (
	"fmt"
	"net/http"
	"strings"
)

type Message struct {
	Text    string
	Headers map[string]string
}

type _ntfy struct {
	url string
}

func (n _ntfy) Send(message Message) error {
	fmt.Println(message)
	req, _ := http.NewRequest("POST", n.url, strings.NewReader(message.Text))
	for k, v := range message.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	fmt.Println(resp)
	return nil
}

type Sender interface {
	Send(Message) error
}

func Create(topic string) Sender {
	ntfy := _ntfy{url: "https://ntfy.sh/" + topic}
	return ntfy
}

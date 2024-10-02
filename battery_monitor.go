// /usr/bin/true; exec /usr/bin/env go run "$0" "$@"
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/nicois/battery_monitor/ntfy"
	"github.com/nicois/battery_monitor/power_sources"
	"go.uber.org/zap"
)

var logger *zap.Logger

func initLogger(ctx context.Context) {
	// config := zap.NewDevelopmentConfig()
	config := zap.NewProductionConfig()

	config.Level.SetLevel(zap.InfoLevel)

	// from https://pkg.go.dev/time#pkg-constants
	// config.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(time.DateTime)
	// config.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(time.RFC822)

	_logger, err := config.Build()
	if err != nil {
		fmt.Printf("can't initialize zap logger: %v\n", err)
	}
	logger = _logger
}

type Alerter interface {
	Alerted(power_sources.Status)
	ShouldAlert(*zap.Logger, *power_sources.Status) (bool, string)
}

type Sender interface {
	Send(ctx context.Context, logger *zap.Logger, message ntfy.Message) error
}

type PowerSource interface {
	GetStatus(ctx context.Context) (*power_sources.Status, error)
}

func monitor[P PowerSource, A Alerter, S Sender](ctx context.Context, p P, a A, s S) {
	ticker := time.NewTicker(time.Minute)

	tagsmap := map[string]string{"max": "skull", "min": "partying_face", "high": "triangular_flag_on_post"}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	var previousStatus *power_sources.Status
	for {
		status, err := p.GetStatus(ctx)
		if err != nil {
			logger.Warn("While getting charge", zap.Error(err))
			time.Sleep(time.Minute * 10)
			continue
		}
		if previousStatus == nil {
			previousStatus = status
		}
		if ratio := previousStatus.Charge() / status.Charge(); ratio <= 0.99 || ratio >= 1.01 {
			logger.Info("current status", zap.Inline(*status))
			previousStatus = status
		}
		if ok, priority := a.ShouldAlert(logger, status); ok {
			headers := map[string]string{"Priority": priority}
			if tags, ok := tagsmap[priority]; ok {
				headers["Tags"] = tags
			}
			body := fmt.Sprintf("Level is %v", status)
			if hostname != "" {
				headers["Title"] = hostname
			}
			if err = s.Send(ctx, logger, ntfy.Message{Text: body, Headers: headers}); err == nil {
				a.Alerted(*status)
			} else {
				logger.Warn("While trying to send an alert", zap.Error(err))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			break

		}
	}
}

type Config struct {
	Topic string
}

func get_config() Config {
	usr, _ := user.Current()
	dir := usr.HomeDir
	filename := dir + "/.config/battery_monitor.toml"
	data, err := os.ReadFile(filename)
	if err != nil {
		panic(err)
	}
	config := Config{}
	_, err = toml.Decode(string(data), &config)
	if err != nil {
		panic(err)
	}
	if len(config.Topic) < 1 {
		panic("you have not defined a topic")
	}
	return config
}

func main() {
	ctx := context.Background()
	initLogger(ctx)
	defer func() {
		if logger != nil {
			if err := logger.Sync(); !errors.Is(err, syscall.EINVAL) {
				fmt.Println(err)
			}
		}
	}()

	config := get_config()
	sender := ntfy.Create(config.Topic)
	battery := power_sources.Battery()
	status, err := battery.GetStatus(ctx)
	if err != nil {
		panic(err)
	}
	alerter := power_sources.CreateNormalAlerter(*status)
	monitor(ctx, battery, alerter, sender)
}

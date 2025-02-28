// /usr/bin/true; exec /usr/bin/env go run "$0" "$@"
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/nicois/battery_monitor/ntfy"
	"github.com/nicois/battery_monitor/power_sources"

	"go.uber.org/zap"

	_ "github.com/joho/godotenv/autoload"
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

func monitor[P power_sources.PowerSource](ctx context.Context, p P, once bool) {
	ticker := time.NewTicker(time.Minute)
	haToken := os.Getenv("HA_REST_API_TOKEN")
	sensor := os.Getenv("HA_SENSOR")
	ha := NewHomeAssistantRestApi("https://qck.duckdns.org", haToken)
	for {
		status, err := p.GetStatus(ctx)
		if err != nil {
			logger.Warn("While getting charge", zap.Error(err))
			time.Sleep(time.Minute * 10)
			continue
		}
		ha.UpdateNumericState(
			ctx,
			sensor,
			float32(100*status.Charge()),
			"%",
			2,
		)
		if once {
			return
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
	var once = flag.Bool("once", true, "only run a single time")
	flag.Parse()

	initLogger(ctx)
	defer func() {
		if logger != nil {
			if err := logger.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTTY) {
				fmt.Println(err)
			}
		}
	}()

	battery := power_sources.NewBattery()
	monitor(ctx, battery, *once)
}

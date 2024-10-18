// /usr/bin/true; exec /usr/bin/env go run "$0" "$@"
package main

import (
	"context"
	"net/url"
	"sync"
	"errors"
	"fmt"
	"os"
	"os/user"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/nicois/battery_monitor/ntfy"
	"github.com/nicois/battery_monitor/power_sources"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"go.uber.org/zap"
)

var logger *zap.Logger

type Mqtt struct {
	topic string
	connectionMutex *sync.Mutex
	connection *autopaho.ConnectionManager
}

func NewMqtt(ctx context.Context, topic string) *Mqtt {
	topicPrefix := os.Getenv("MQTT_TOPIC_PREFIX")
	clientID := os.Getenv("MQTT_CLIENT_ID")
	if topicPrefix == "" || clientID == "" {
		logger.Info("Not enabling MQTT as topic and/or client ID are not defined")
		return &Mqtt{connectionMutex: new(sync.Mutex), connection: nil, topic:""}
	}
	mqtt := &Mqtt{connectionMutex: new(sync.Mutex), connection: nil, topic:topicPrefix + ":" + topic}
	go mqtt.run(ctx, clientID)
	return mqtt
}

func (m *Mqtt) Stop(ctx context.Context) {
	m.connectionMutex.Lock()
	defer m.connectionMutex.Unlock()
	if m.connection != nil {
		if err := m.connection.Disconnect(ctx) ; err != nil {
			logger.Info("Problem disconnecting MQTT", zap.Error(err))
		}
	}

}

func (m *Mqtt) Send(ctx context.Context, payload []byte) {
	m.connectionMutex.Lock()
	defer m.connectionMutex.Unlock()
	if m.connection == nil {
		logger.Info("No connection exists, so discarding the payload", zap.String("payload", string(payload)))
		return
	}
	m.connection.PublishViaQueue(ctx, &autopaho.QueuePublish{Publish: &paho.Publish{QoS: 1, Topic: m.topic, Payload: payload}})
}

func (m *Mqtt) run(ctx context.Context, clientID string) {
	// We will connect to the Eclipse test server (note that you may see messages that other users publish)
	u, err := url.Parse("mqtt://192.168.4.5:1883")
	if err != nil {
		logger.Error("cannot resolve MQTT server connection address", zap.Error(err))
		return
	}

	cliCfg := autopaho.ClientConfig{
		ConnectUsername: "potato",
		ConnectPassword: []byte("potato"),
		ServerUrls: []*url.URL{u},
		KeepAlive:  20, // Keepalive message should be sent every 20 seconds
		// CleanStartOnInitialConnection defaults to false. Setting this to true will clear the session on the first connection.
		CleanStartOnInitialConnection: false,
		// SessionExpiryInterval - Seconds that a session will survive after disconnection.
		// It is important to set this because otherwise, any queued messages will be lost if the connection drops and
		// the server will not queue messages while it is down. The specific setting will depend upon your needs
		// (60 = 1 minute, 3600 = 1 hour, 86400 = one day, 0xFFFFFFFE = 136 years, 0xFFFFFFFF = don't expire)
		SessionExpiryInterval: 60,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, connAck *paho.Connack) {
			logger.Info("mqtt connection up")
			// Subscribing in the OnConnectionUp callback is recommended (ensures the subscription is reestablished if
			// the connection drops)
			if _, err := cm.Subscribe(context.Background(), &paho.Subscribe{
				Subscriptions: []paho.SubscribeOptions{
					{Topic: m.topic, QoS: 1},
				},
			}); err != nil {
				logger.Info("failed to subscribe. This is likely to mean no messages will be received.", zap.Error(err))
			}
			logger.Info("mqtt subscription made")
		},
		OnConnectError: func(err error) { fmt.Printf("error whilst attempting connection: %s\n", err) },
		// eclipse/paho.golang/paho provides base mqtt functionality, the below config will be passed in for each connection
		ClientConfig: paho.ClientConfig{
			// If you are using QOS 1/2, then it's important to specify a client id (which must be unique)
			ClientID: clientID,
			// OnPublishReceived is a slice of functions that will be called when a message is received.
			// You can write the function(s) yourself or use the supplied Router
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					logger.Info("received message", zap.String("topic", pr.Packet.Topic), zap.String("client ID", pr.Client.ClientID()), zap.String("payload", string(pr.Packet.Payload)), zap.Bool("retain", pr.Packet.Retain))
					return true, nil
				}},
			OnClientError: func(err error) { fmt.Printf("client error: %s\n", err) },
			OnServerDisconnect: func(d *paho.Disconnect) {
				if d.Properties != nil {
					fmt.Printf("server requested disconnect: %s\n", d.Properties.ReasonString)
				} else {
					fmt.Printf("server requested disconnect; reason code: %d\n", d.ReasonCode)
				}
			},
		},
	}

	c, err := autopaho.NewConnection(ctx, cliCfg) // starts process; will reconnect until context cancelled
	if err != nil {
		logger.Error("cannot start new MQTT connection", zap.Error(err))
		return
	}
	m.connectionMutex.Lock()
	defer m.connectionMutex.Unlock()
	m.connection = c
}

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

	mqtt := NewMqtt(ctx, "battery-monitor")
	defer mqtt.Stop(ctx)
	tagsmap := map[string]string{"max": "skull", "min": "partying_face", "high": "triangular_flag_on_post"}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	var previousStatus *power_sources.Status
	for {
		status, err := p.GetStatus(ctx)
		mqtt.Send(ctx, []byte(fmt.Sprintf("%v", status.Charge())))
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

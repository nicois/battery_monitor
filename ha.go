package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type LastValue struct {
	value  []byte
	expiry time.Time
}

func (lv LastValue) IsSame(value []byte) bool {
	return time.Since(lv.expiry) < 0 && bytes.Equal(lv.value, value)
}

// SensorTolerance indicates when to consider that two values
// are close enough to not be worth reporting the new value of.
// At least one of the thresholds needs to be breached, not both.
type SensorTolerance struct {
	Absolute float32 // must be >0 to ever match
	Relative float32 // must be >1 to ever match
}

func (st SensorTolerance) CloseEnough(oldValue, newValue float32) bool {
	if st.Absolute <= 0 && st.Relative <= 1 {
		// neither condition is set, so always consider values to be different
		return false
	}
	if st.Absolute > 0 {
		var difference float32
		if oldValue > newValue {
			difference = oldValue - newValue
		} else {
			difference = newValue - oldValue
		}
		if difference > st.Absolute {
			return false
		}
	}
	if st.Relative > 1 {
		if oldValue == 0 {
			// infinite ratio
			return false
		}
		ratio := newValue / oldValue
		if ratio < 0 {
			ratio = -ratio
		}
		if ratio < 1 {
			ratio = 1 / ratio
		}
		if ratio > st.Relative {
			return false
		}
	}
	return true
}

type HaRestApi struct {
	readOnly           bool
	readTimeout        time.Duration
	writeTimeout       time.Duration
	valueCacheDuration time.Duration
	server             string
	token              string
	client             *http.Client
	lastValues         map[string]LastValue
	lastNumericValues  map[string]float32
	numericTolerances  map[string]SensorTolerance
}

type haOption func(h *HaRestApi) error

func WithReadOnly(h *HaRestApi) error {
	h.readOnly = true
	return nil
}

func WithTolerance(sensor string, tolerance SensorTolerance) haOption {
	return func(h *HaRestApi) error {
		h.numericTolerances[sensor] = tolerance
		return nil
	}
}

func NewHomeAssistantRestApi(server, token string, options ...haOption) *HaRestApi {
	result := &HaRestApi{
		readOnly:           false, // disregard attempts to send updates
		readTimeout:        5 * time.Second,
		valueCacheDuration: time.Hour, // retain a record of transmitted values for this long
		writeTimeout:       5 * time.Second,
		token:              token, // auth token
		client:             &http.Client{},
		server:             server, // URL
		lastValues:         make(map[string]LastValue),
		lastNumericValues:  make(map[string]float32),
		numericTolerances:  make(map[string]SensorTolerance),
	}
	for _, option := range options {
		Must0(option(result))
	}
	return result
}

type HaAttributes struct {
	UnitOfMeasurement string `json:"unit_of_measurement,omitempty"`
}

type HaRestMessage struct {
	State      string       `json:"state"`
	Attributes HaAttributes `json:"attributes"`
}

// MarkAllUnavailable will set all known sensor values to
// 'unavailable'. This prevents Home Assistant from incorrectly
// thinking a value is unchanged when it is no longer getting updates.
func (a *HaRestApi) MarkAllUnavailable(
	ctx context.Context,
) {
	wg := &sync.WaitGroup{}
	for sensor_ := range a.lastValues {
		sensor := sensor_
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := a.UpdateState(
				ctx,
				sensor,
				HaRestMessage{
					State: "unavailable",
				},
			)
			if err != nil {
				logger.Info(
					"unable to set sensor to unavailable",
					zap.String("sensor", sensor),
					zap.Error(err),
				)
			} else {
				logger.Info(
					"set sensor to unavailable",
					zap.String("sensor", sensor),
				)
			}
		}()
	}
	wg.Wait()
}

func (a *HaRestApi) MarkUnavailable(
	ctx context.Context,
	sensor string,
) error {
	return a.UpdateState(
		ctx,
		sensor,
		HaRestMessage{
			State: "unavailable",
		},
	)
}

func (a *HaRestApi) LastNumericState(sensor string) (float32, bool) {
	value, exists := a.lastNumericValues[sensor]
	return value, exists
}

func (a *HaRestApi) UpdateNumericState(
	ctx context.Context,
	sensor string,
	value float32,
	unit string,
	precision int,
) error {
	if tolerance, exists := a.numericTolerances[sensor]; exists {
		if lastNumericValue, exists := a.lastNumericValues[sensor]; exists &&
			tolerance.CloseEnough(lastNumericValue, value) {
			logger.Debug(
				"not sending new value as it's too close to the old one",
				zap.String("sensor", sensor),
				zap.Float32("new", value),
				zap.Float32("old", lastNumericValue),
			)
			return nil
		}
	}
	state := fmt.Sprintf(fmt.Sprintf("%%.%vf", precision), value)
	err := a.UpdateState(
		ctx,
		sensor,
		HaRestMessage{
			State:      state,
			Attributes: HaAttributes{UnitOfMeasurement: unit},
		},
	)
	if err == nil {
		a.lastNumericValues[sensor] = value
	}
	return err
}

func (a *HaRestApi) GetNumericState(
	ctx context.Context,
	sensor string,
	unit string,
) (float32, error) {
	state, err := a.GetState(ctx, sensor)
	if err != nil {
		return 0, err
	}
	if unit != "" {
		if attr := state.Attributes.UnitOfMeasurement; attr != unit {
			return 0, fmt.Errorf("expected %v, found %v", unit, attr)
		}
	}
	value, err := strconv.ParseFloat(state.State, 32)
	if err != nil {
		return 0, err
	}
	return float32(value), nil
}

// MonitorNumericState polls a sensor's state, verifying it
// has the nominated unit. If a (sufficiently) different value
// is detected, the returned atomic pointer's value is updated,
// and (if not nil), an attempt is made to push the new value
// to the specified channel.
// Cancelling the provided context will terminate the polling operation.
func (a *HaRestApi) MonitorNumericState(
	ctx context.Context,
	pollingPeriod time.Duration,
	sensor string,
	unit string,
	c chan<- float32,
) *atomic.Int64 {
	var value atomic.Int64
	go func() {
		ticker := time.NewTicker(pollingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if newValue, err := a.GetNumericState(ctx, sensor, unit); err == nil {
					if tolerance, exists := a.numericTolerances[sensor]; exists {
						if tolerance.CloseEnough(float32(value.Load()), newValue) {
							logger.Debug(
								"monitor: ignore new value as it's too close to the old one",
								zap.String("sensor", sensor),
								zap.Float32("new", newValue),
								zap.Int64("old", value.Load()),
							)
							continue
						}
					}
					value.Store(int64(newValue))
					if c != nil {
						select {
						case c <- newValue:
						default:
						}
					}
				} else {
					logger.Info("problem polling sensor", zap.String("sensor", sensor), zap.Error(err))
				}
			}
		}
	}()
	return &value
}

func (a *HaRestApi) GetState(ctx context.Context, sensor string) (HaRestMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.readTimeout)
	defer cancel()
	result := HaRestMessage{}
	url := fmt.Sprintf("https://qck.duckdns.org/api/states/%v", sensor)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return result, err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", a.token))
	req.Header.Add("content-type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	dec := json.NewDecoder(response.Body)
	if err := dec.Decode(&result); err != nil {
		response2, err3 := a.client.Do(req)
		if err3 != nil {
			logger.Warn("re-read failed", zap.Error(err3))
			return result, err
		}
		defer response2.Body.Close()
		respBytes, err2 := ioutil.ReadAll(response.Body)
		if err2 != nil {
			logger.Warn("re-read failed", zap.Error(err2))
			return result, err2
		} else {
			logger.Warn("decoding error", zap.String("body", string(respBytes)))
		}
		return result, err
	}

	return result, nil
}

func (a *HaRestApi) UpdateState(ctx context.Context, sensor string, message HaRestMessage) error {
	ctx2, cancel := context.WithTimeout(ctx, a.writeTimeout)
	defer cancel()
	payloadBuf := new(bytes.Buffer)
	enc := json.NewEncoder(payloadBuf)
	err := enc.Encode(message)
	if err != nil {
		return err
	}
	payload := payloadBuf.Bytes()
	url := fmt.Sprintf("https://qck.duckdns.org/api/states/%v", sensor)
	if previous, exists := a.lastValues[sensor]; exists && previous.IsSame(payload) {
		logger.Debug(
			"not updating as the value has not changed",
			zap.String("URL", url),
			zap.String("message", string(payload)))
		return nil
	}
	if a.readOnly {
		logger.Info(
			"would normally send message",
			zap.String("URL", url),
			zap.String("message", string(payload)))
		return nil
	}
	req, err := http.NewRequestWithContext(ctx2, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", a.token))
	req.Header.Add("content-type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return err
	}
	respBytes, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return err
	}
	response.Body.Close()
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		logger.Warn(
			"unexpected response",
			zap.String("URL", url),
			zap.Int("status code", response.StatusCode),
			zap.String("message", string(payload)),
			zap.String("response", string(respBytes)),
		)
		return fmt.Errorf("Unexpected response %v", response.StatusCode)
	}
	a.lastValues[sensor] = LastValue{value: payload, expiry: time.Now().Add(a.valueCacheDuration)}
	return nil
}

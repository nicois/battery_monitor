package power_sources

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type NormalAlerter struct {
	lastStatus Status
}

type PowerSource interface {
	GetStatus(ctx context.Context) (*Status, error)
}

func (a NormalAlerter) ShouldAlert(logger *zap.Logger, newStatus *Status) (bool, string) {
	logger.Debug("checking", zap.Object("new", *newStatus), zap.Object("previous", a.lastStatus))
	if newStatus.state == a.lastStatus.state && newStatus.charge >= a.lastStatus.charge &&
		newStatus.charge < 0.80 {
		return false, ""
	}
	if newStatus.charge*1.05 <= a.lastStatus.charge {
		if newStatus.charge < 0.40 {
			return true, "max"
		}
		if newStatus.charge < 0.45 {
			return true, "high"
		}
		if newStatus.charge < 0.50 {
			return true, "default"
		}
		if newStatus.charge < 0.60 {
			return true, "low"
		}
		if newStatus.charge < 0.80 {
			return false, "min"
		}
	}
	if newStatus.charge >= a.lastStatus.charge+.1 {
		return true, "min"
	}
	if newStatus.charge >= 0.80 && a.lastStatus.charge < 0.80 {
		return true, "default"
	}
	if newStatus.timestamp.Sub(a.lastStatus.timestamp) >= time.Hour*4 {
		if newStatus.charge > 0.80 {
			return true, "default"
		}
	}
	return false, ""
}

func (a *NormalAlerter) Alerted(status Status) {
	a.lastStatus = status
}

func CreateNormalAlerter(initialStatus Status) *NormalAlerter {
	return &NormalAlerter{lastStatus: initialStatus}
}

type battery struct {
	path    string
	total   float64
	flavour string
}

func NewBattery() PowerSource {
	mb := &macBattery{}
	if _, err := mb.GetStatus(context.Background()); err == nil {
		return mb
	} else {
		fmt.Printf("mac error: %v\n", err)
	}
	bats := Must(filepath.Glob("/sys/class/power_supply/BAT*"))
	for _, bat := range bats {
		b := &battery{path: bat}
		for _, potentialFlavour := range []string{"energy", "charge"} {
			b.flavour = potentialFlavour
			if total, err := b.getFullLevel(); err == nil {
				b.total = total
				return b
			}
		}
	}

	panic("Could not read the battery level")
}

type Status struct {
	charge    float64
	state     string
	timestamp time.Time
}

func (s Status) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddFloat64("charge", s.charge)
	enc.AddString("state", s.state)
	enc.AddTime("timestamp", s.timestamp)
	return nil
}

func (s Status) String() string {
	return fmt.Sprintf("%.0f%% [%v]", s.charge*100, s.state)
}

func (s Status) State() string {
	return s.state
}

func (s Status) Charge() float64 {
	return s.charge
}

func (s Status) Time() time.Time {
	return s.timestamp
}

func (b battery) getFullLevel() (float64, error) {
	byteValue, err := os.ReadFile(
		fmt.Sprintf("%v/%v_full_design", b.path, b.flavour),
	)
	if err == nil {
		return strconv.ParseFloat(strings.TrimSpace(string(byteValue)), 64)
	}
	return 0, err
}

func (b battery) getCurrentLevel() (float64, error) {
	byteValue, err := os.ReadFile(fmt.Sprintf("%v/%v_now", b.path, b.flavour))
	if err == nil {
		return strconv.ParseFloat(strings.TrimSpace(string(byteValue)), 64)
	}
	return 0, err
}

func (b *battery) GetStatus(ctx context.Context) (*Status, error) {
	result := &Status{charge: -1, state: "", timestamp: time.Now()}
	if status, err := os.ReadFile(fmt.Sprintf("%v/status", b.path)); err == nil {
		result.state = strings.TrimSpace(string(status))
	}

	if currentLevel, err := b.getCurrentLevel(); err == nil {
		if currentLevel > 0 {
			result.charge = currentLevel / b.total
		} else {
			return nil, fmt.Errorf("Zero charge!")
		}
	} else {
		return nil, err
	}

	return result, nil
}

type macBattery struct{}

func (b *macBattery) GetStatus(ctx context.Context) (*Status, error) {
	/*
			Now drawing from 'AC Power'
		 -InternalBattery-0 (id=5963875)	80%; AC attached; not charging present: true
	*/
	out, err := exec.Command("/usr/bin/pmset", "-g", "batt").Output()
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?s)Now drawing from '(.*?)'.+\s(\d+)%;`)
	matches := re.FindStringSubmatch(string(out))
	if matches == nil {
		return nil, fmt.Errorf("%q cannot be matched", out)
	}
	charge, err := strconv.ParseFloat(matches[2], 64)
	if err != nil {
		return nil, fmt.Errorf("%q cannot be parsed as a float", matches[2])

	}
	result := Status{timestamp: time.Now(), state: matches[1], charge: charge / 100}
	return &result, nil
}

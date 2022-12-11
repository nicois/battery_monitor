///usr/bin/true; exec /usr/bin/env go run "$0" "$@"
package power_sources

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type PowerSource interface {
	Get_charge() (int, error)
}

type NormalAlerter struct {
	last_alert_time  time.Time
	last_alert_level int
}

func (a NormalAlerter) ShouldAlert(new_level int) (bool, string) {
	if new_level < 40 && a.last_alert_level-new_level > 5 {
		return true, "max"
	}
	if new_level < 60 && a.last_alert_level-new_level > 5 {
		return true, "high"
	}
	if new_level < 80 && a.last_alert_level-new_level > 10 {
		return true, "normal"
	}
	if time.Since(a.last_alert_time) > time.Hour*24 {
		return true, "min"
	}
	return false, ""
}

func (a *NormalAlerter) Alerted(level int) {
	a.last_alert_time = time.Now()
	a.last_alert_level = level
}

type Alerter interface {
	Alerted(int)
	ShouldAlert(int) (bool, string)
}

type Battery struct {
	total float64
}

func (b *Battery) Get_charge() (int, error) {
	now, err := os.ReadFile("/sys/class/power_supply/BAT0/energy_now")
	if err != nil {
		return -1, err
	}
	now_float, err := strconv.ParseFloat(strings.TrimSpace(string(now)), 64)
	if err != nil {
		return -1, err
	}

	if b.total == 0 {
		total, err := os.ReadFile("/sys/class/power_supply/BAT0/energy_full")
		if err != nil {
			return -1, err
		}
		b.total, err = strconv.ParseFloat(strings.TrimSpace(string(total)), 64)
		if err != nil {
			return -1, err
		}
	}
	return int(100 * now_float / b.total), nil
}

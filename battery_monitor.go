///usr/bin/true; exec /usr/bin/env go run "$0" "$@"
package main

import (
	"fmt"
	"log"
	"nicois/battery_monitor/ntfy"
	"nicois/battery_monitor/power_sources"
	"os"
	"os/user"
	"time"

	"github.com/BurntSushi/toml"
)

func monitor[P power_sources.PowerSource, A power_sources.Alerter, S ntfy.Sender](p P, a A, s S) {
	tagsmap := map[string]string{"max": "skull", "min": "partying_face", "high": "triangular_flag_on_post"}
	for {
		level, err := p.Get_charge()
		if err == nil {
			if ok, priority := a.ShouldAlert(level); ok {
				headers := map[string]string{"Priority": priority}
				if tags, ok := tagsmap[priority]; ok {
					headers["Tags"] = tags
				}
				err = s.Send(ntfy.Message{Text: fmt.Sprintf("Level is %v", level), Headers: headers})
				if err == nil {
					a.Alerted(level)
				}
			}
		}
		if err != nil {
			log.Println(err)
		}
		time.Sleep(time.Minute)
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
	config := get_config()
	sender := ntfy.Create(config.Topic)
	battery := &power_sources.Battery{}
	alerter := &power_sources.NormalAlerter{}
	monitor(battery, alerter, sender)
}

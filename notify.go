package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"time"
)

// notifier runs a shell command for grid events so the user gets a desktop
// (or any other) notification. The command receives EVENT_KIND, EVENT_MESSAGE
// and EVENT_URGENCY in its environment.
type notifier struct {
	cmd string
}

func (n *notifier) OnEvent(e Event) {
	if n == nil || n.cmd == "" {
		return
	}
	urgency := "normal"
	switch e.Kind {
	case "grid_lost", "low_battery":
		urgency = "critical"
	case "grid_restored":
		urgency = "normal"
	default:
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "/bin/sh", "-c", n.cmd)
		c.Env = append(os.Environ(),
			"EVENT_KIND="+e.Kind,
			"EVENT_MESSAGE="+e.Message,
			"EVENT_URGENCY="+urgency,
			"EVENT_TIME="+e.Time.Format("15:04:05"),
		)
		if out, err := c.CombinedOutput(); err != nil {
			log.Printf("notify command failed: %v: %s", err, out)
		}
	}()
}

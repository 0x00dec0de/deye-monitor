package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

const defaultNotifyCmd = `notify-send -a "Deye UPS" -u "$EVENT_URGENCY" -i battery "Deye: $EVENT_MESSAGE" "$EVENT_TIME"`

func main() {
	addr := flag.String("addr", envOr("LISTEN_ADDR", ":8080"), "HTTP listen address")
	inverter := flag.String("inverter", envOr("INVERTER_ADDR", ""), "Solarman logger host:port, e.g. 192.168.1.50:8899 (required)")
	serial := flag.Uint64("serial", envUint("LOGGER_SERIAL", 0), "Solarman logger serial number, 10 digits from the stick sticker or its web UI (required)")
	slave := flag.Uint("slave", uint(envUint("MODBUS_SLAVE", 1)), "Modbus slave id")
	interval := flag.Duration("interval", envDur("POLL_INTERVAL", 10*time.Second), "poll interval")
	history := flag.Duration("history", envDur("HISTORY", 48*time.Hour), "how much history to keep in memory")
	dataFile := flag.String("data", envOr("DATA_FILE", "history.json"), "file to persist history/events ('' to disable)")
	notifyCmd := flag.String("notify-cmd", envOr("NOTIFY_CMD", defaultNotifyCmd),
		"shell command run on grid lost/restored and low battery; gets EVENT_KIND, EVENT_MESSAGE, EVENT_URGENCY, EVENT_TIME in env ('' to disable)")
	flag.Parse()
	if *inverter == "" || *serial == 0 {
		flag.Usage()
		log.Fatal("both -inverter and -serial are required")
	}

	client := NewSolarmanClient(*inverter, uint32(*serial), byte(*slave), 8*time.Second)
	store := NewStore(*dataFile, *history)
	store.onEvent = (&notifier{cmd: *notifyCmd}).OnEvent

	go poll(client, store, *interval)
	go func() {
		for range time.Tick(time.Minute) {
			store.Save()
		}
	}()

	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, store.Snapshot())
	})
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		window := 6 * time.Hour
		if d, err := time.ParseDuration(r.URL.Query().Get("range")); err == nil && d > 0 && d <= *history {
			window = d
		}
		writeJSON(w, store.History(window, 600))
	})

	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		log.Printf("listening on %s, polling %s every %s", *addr, *inverter, *interval)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	store.Save()
	_ = srv.Close()
}

func poll(c *SolarmanClient, store *Store, interval time.Duration) {
	for {
		st, err := ReadStatus(c)
		if err != nil {
			log.Printf("poll: %v", err)
			store.Fail(err)
		} else {
			store.Update(st)
		}
		time.Sleep(interval)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envUint(k string, def uint64) uint64 {
	if v, err := strconv.ParseUint(os.Getenv(k), 10, 64); err == nil {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

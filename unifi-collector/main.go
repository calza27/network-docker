package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

const (
	maxBodyBytes    = 1 << 20 // 1 MiB
	cameraNamesPath = "/var/lib/cameras.yml"
)

// local cache of events
// "alarm name": {"camera friendly name": count}
// guarded by metricsMu, as postData and getMetrics run concurrently
var (
	metricsMu    sync.Mutex
	metricsCache = map[string]map[string]int64{}
)

// HELP text per event; events not listed here get a generic description
var metricHelp = map[string]string{
	"person_detected":   "A count of the number of people detected",
	"vehicle_detected":  "A count of the number of vehicles detected",
	"animal_detected":   "A count of the number of animals detected",
	"speaking_detected": "A count of the number of times a person speaking was detected",
	"smoke_alarm":       "A count of the number of times a smoke alarm was detected",
}

var metricNameRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

var labelEscaper = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n")

// camera ID -> friendly name, loaded once on startup and read-only after
var cameraNames map[string]string

type unifiPayload struct {
	Alarm struct {
		Name    string `json:"name"`
		Sources []struct {
			Device string `json:"device"`
		} `json:"sources"`
	} `json:"alarm"`
}

func main() {
	port := os.Getenv("LISTEN_PORT")
	if port == "" {
		log.Fatal("LISTEN_PORT environment variable is not set")
	}

	names, err := loadCameraNames(cameraNamesPath)
	if err != nil {
		log.Fatalf("loading camera names: %v", err)
	}
	cameraNames = names
	log.Printf("loaded %d camera names from %s", len(cameraNames), cameraNamesPath)

	authKey, err := loadAuthKey()
	if err != nil {
		log.Fatalf("loading auth key: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /metrics", requireBearer(authKey, postData))
	mux.HandleFunc("GET /metrics", getMetrics)

	addr := ":" + port
	log.Printf("unifi-collector listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// loadAuthKey reads the bearer token from the file named by AUTHKEY_FILE,
// falling back to the value of AUTHKEY.
func loadAuthKey() (string, error) {
	key := os.Getenv("AUTHKEY")
	if path := os.Getenv("AUTHKEY_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		// secret files usually end with a newline
		key = strings.TrimSpace(string(data))
	}

	if key == "" {
		return "", errors.New("neither AUTHKEY_FILE nor AUTHKEY provided a key")
	}
	return key, nil
}

// requireBearer rejects requests whose Authorization header is not "Bearer <token>".
func requireBearer(token string, next http.HandlerFunc) http.HandlerFunc {
	want := []byte(token)
	return func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="unifi-collector"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// postData accepts a JSON body pushed to the collector.
func postData(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("payload received: %s", body)

	var payload unifiPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// the alarm name becomes the metric name, so an invalid one would break the whole scrape
	event := payload.Alarm.Name
	if !metricNameRe.MatchString(event) {
		log.Printf("rejecting event: alarm name %q is not a valid metric name", event)
		http.Error(w, "alarm.name must match "+metricNameRe.String(), http.StatusBadRequest)
		return
	}
	if len(payload.Alarm.Sources) == 0 {
		log.Printf("rejecting event %q: no sources", event)
		http.Error(w, "alarm.sources must not be empty", http.StatusBadRequest)
		return
	}

	camera := cameraName(payload.Alarm.Sources[0].Device)
	recordEvent(event, camera)
	log.Printf("recorded event %q from camera %q (%q)", event, camera, payload.Alarm.Sources[0].Device)

	w.WriteHeader(http.StatusAccepted)
}

// loadCameraNames reads a YAML map of camera ID to friendly name.
func loadCameraNames(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	names := map[string]string{}
	if err := yaml.Unmarshal(data, &names); err != nil {
		return nil, err
	}
	return names, nil
}

// cameraName returns the friendly name for a camera ID, falling back to the ID itself.
func cameraName(id string) string {
	if name, ok := cameraNames[id]; ok {
		return name
	}
	return id
}

// recordEvent increments the count for an event from a camera.
func recordEvent(event, camera string) {
	metricsMu.Lock()
	defer metricsMu.Unlock()

	if metricsCache[event] == nil {
		metricsCache[event] = map[string]int64{}
	}
	metricsCache[event][camera]++
}

// getMetrics writes metricsCache in Prometheus text format, then empties it.
func getMetrics(w http.ResponseWriter, r *http.Request) {
	// swap the cache out under the lock so writing the response doesn't block postData
	metricsMu.Lock()
	snapshot := metricsCache
	metricsCache = map[string]map[string]int64{}
	metricsMu.Unlock()

	var b strings.Builder
	for _, event := range slices.Sorted(maps.Keys(snapshot)) {
		help, ok := metricHelp[event]
		if !ok {
			help = "A count of " + event + " events"
		}
		fmt.Fprintf(&b, "# HELP %s %s\n", event, help)
		// gauge rather than counter: values are per-scrape deltas, as the cache is reset on every GET
		fmt.Fprintf(&b, "# TYPE %s gauge\n", event)

		cameras := snapshot[event]
		for _, camera := range slices.Sorted(maps.Keys(cameras)) {
			fmt.Fprintf(&b, "%s{camera=\"%s\"} %d\n", event, labelEscaper.Replace(camera), cameras[camera])
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.String()))
}

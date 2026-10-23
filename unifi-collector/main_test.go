package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMain(m *testing.M) {
	// postData logs every event; keep test output readable
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// resetState clears the package-level state shared between handlers.
func resetState(t *testing.T) {
	t.Helper()
	metricsMu.Lock()
	metricsCache = map[string]map[string]int64{}
	metricsMu.Unlock()
	cameraNames = map[string]string{
		"camera_1": "front door",
		"camera_2": "back door",
	}
}

func writeFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// event builds a payload triggered by device. sources lists every camera the
// alarm covers, with camera_2 first, to catch reading sources instead of triggers.
func event(name, device string) string {
	return fmt.Sprintf(`{"alarm":{"name":%q,"sources":[{"device":"camera_2","type":"include"},{"device":%q,"type":"include"}],"triggers":[{"key":"person","device":%q}]},"timestamp":1700000000}`, name, device, device)
}

func post(body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	postData(rec, httptest.NewRequest(http.MethodPost, "/metrics", strings.NewReader(body)))
	return rec
}

func scrape() *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	getMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec
}

func TestLoadCameraNames(t *testing.T) {
	t.Run("valid file", func(t *testing.T) {
		path := writeFile(t, "---\ncamera_1: \"front door\"\ncamera_2: \"back door\"\n")

		names, err := loadCameraNames(path)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"camera_1": "front door", "camera_2": "back door"}
		if len(names) != len(want) {
			t.Fatalf("got %v, want %v", names, want)
		}
		for id, name := range want {
			if names[id] != name {
				t.Errorf("names[%q] = %q, want %q", id, names[id], name)
			}
		}
	})

	t.Run("empty file", func(t *testing.T) {
		names, err := loadCameraNames(writeFile(t, ""))
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 0 {
			t.Errorf("got %v, want empty map", names)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := loadCameraNames(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
			t.Error("want error, got nil")
		}
	})

	t.Run("not a map", func(t *testing.T) {
		if _, err := loadCameraNames(writeFile(t, "- camera_1\n- camera_2\n")); err == nil {
			t.Error("want error, got nil")
		}
	})
}

func TestCameraName(t *testing.T) {
	resetState(t)

	tests := map[string]string{
		"camera_1":    "front door",
		"camera_2":    "back door",
		"unknown_cam": "unknown_cam",
		"":            "",
	}
	for id, want := range tests {
		if got := cameraName(id); got != want {
			t.Errorf("cameraName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestLoadAuthKey(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		file    *string // nil means AUTHKEY_FILE is unset
		want    string
		wantErr bool
	}{
		{name: "env only", env: "from-env", want: "from-env"},
		{name: "file only", file: ptr("from-file"), want: "from-file"},
		{name: "file trims whitespace", file: ptr("  from-file\n"), want: "from-file"},
		{name: "file takes precedence", env: "from-env", file: ptr("from-file"), want: "from-file"},
		{name: "neither set", wantErr: true},
		{name: "empty file", env: "from-env", file: ptr("\n"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AUTHKEY", tt.env)
			t.Setenv("AUTHKEY_FILE", "")
			if tt.file != nil {
				t.Setenv("AUTHKEY_FILE", writeFile(t, *tt.file))
			}

			got, err := loadAuthKey()
			if tt.wantErr {
				if err == nil {
					t.Errorf("want error, got key %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		t.Setenv("AUTHKEY", "from-env")
		t.Setenv("AUTHKEY_FILE", filepath.Join(t.TempDir(), "missing"))
		if _, err := loadAuthKey(); err == nil {
			t.Error("want error, got nil")
		}
	})
}

func ptr(s string) *string { return &s }

func TestRequireBearer(t *testing.T) {
	handler := requireBearer("s3cret", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{name: "valid token", header: "Bearer s3cret", want: http.StatusAccepted},
		{name: "no header", want: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "token prefix", header: "Bearer s3c", want: http.StatusUnauthorized},
		{name: "missing scheme", header: "s3cret", want: http.StatusUnauthorized},
		{name: "basic scheme", header: "Basic s3cret", want: http.StatusUnauthorized},
		{name: "empty token", header: "Bearer ", want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			handler(rec, req)

			if rec.Code != tt.want {
				t.Errorf("got %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate header on 401")
			}
		})
	}
}

func TestPostData(t *testing.T) {
	t.Run("records events by alarm name and friendly name", func(t *testing.T) {
		resetState(t)

		for _, body := range []string{
			event("person_detected", "camera_1"),
			event("person_detected", "camera_2"),
			event("person_detected", "camera_2"),
			event("animal_detected", "unknown_cam"),
		} {
			if rec := post(body); rec.Code != http.StatusAccepted {
				t.Fatalf("post %s: got %d, want %d", body, rec.Code, http.StatusAccepted)
			}
		}

		want := map[string]map[string]int64{
			"person_detected":  {"front door": 1, "back door": 2},
			"animal_detected": {"unknown_cam": 1},
		}
		if fmt.Sprint(metricsCache) != fmt.Sprint(want) {
			t.Errorf("metricsCache = %v, want %v", metricsCache, want)
		}
	})

	t.Run("records every trigger", func(t *testing.T) {
		resetState(t)

		body := `{"alarm":{"name":"person_detected","triggers":[{"device":"camera_1"},{"device":"camera_2"},{"device":"camera_1"}]}}`
		if rec := post(body); rec.Code != http.StatusAccepted {
			t.Fatalf("got %d, want %d", rec.Code, http.StatusAccepted)
		}

		want := map[string]map[string]int64{"person_detected": {"front door": 2, "back door": 1}}
		if fmt.Sprint(metricsCache) != fmt.Sprint(want) {
			t.Errorf("metricsCache = %v, want %v", metricsCache, want)
		}
	})

	rejected := []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: "nope"},
		{name: "alarm name with space", body: event("Person Sighted", "camera_1")},
		{name: "alarm name starting with digit", body: event("1st_person", "camera_1")},
		{name: "empty alarm name", body: event("", "camera_1")},
		{name: "no alarm", body: `{}`},
		{name: "empty triggers", body: `{"alarm":{"name":"person_detected","triggers":[]}}`},
		{name: "missing triggers", body: `{"alarm":{"name":"person_detected","sources":[{"device":"camera_1"}]}}`},
		{name: "body over size limit", body: `{"alarm":{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}}`},
	}
	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			resetState(t)

			if rec := post(tt.body); rec.Code != http.StatusBadRequest {
				t.Errorf("got %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if len(metricsCache) != 0 {
				t.Errorf("metricsCache = %v, want empty", metricsCache)
			}
		})
	}
}

func TestGetMetrics(t *testing.T) {
	t.Run("renders gauges sorted by event and camera", func(t *testing.T) {
		resetState(t)
		recordEvent("person_detected", "front door")
		for range 3 {
			recordEvent("person_detected", "back door")
		}
		for range 4 {
			recordEvent("animal_detected", "camera_3")
		}

		rec := scrape()

		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want %d", rec.Code, http.StatusOK)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
			t.Errorf("Content-Type = %q", ct)
		}
		// HELP text is read from metricHelp so rewording a description doesn't break this test
		want := fmt.Sprintf(`# HELP animal_detected %s
# TYPE animal_detected gauge
animal_detected{camera="camera_3"} 4
# HELP person_detected %s
# TYPE person_detected gauge
person_detected{camera="back door"} 3
person_detected{camera="front door"} 1
`, metricHelp["animal_detected"], metricHelp["person_detected"])
		if got := rec.Body.String(); got != want {
			t.Errorf("body:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("every known event has HELP text", func(t *testing.T) {
		for event, help := range metricHelp {
			if !metricNameRe.MatchString(event) {
				t.Errorf("metricHelp key %q is not a valid metric name, so UniFi alarms can never use it", event)
			}
			if help == "" || strings.Contains(help, "\n") {
				t.Errorf("metricHelp[%q] = %q: must be non-empty and single-line", event, help)
			}
		}
	})

	t.Run("generic help for unknown events", func(t *testing.T) {
		const event = "not_a_real_event"
		if _, ok := metricHelp[event]; ok {
			t.Fatalf("%s is in metricHelp; pick an event name that isn't", event)
		}
		resetState(t)
		recordEvent(event, "front door")

		want := "# HELP " + event + " A count of " + event + " events\n"
		if got := scrape().Body.String(); !strings.HasPrefix(got, want) {
			t.Errorf("body:\n%s\nwant prefix:\n%s", got, want)
		}
	})

	t.Run("escapes camera label values", func(t *testing.T) {
		resetState(t)
		recordEvent("person_detected", `say "hi"`)
		recordEvent("person_detected", `C:\cam`)
		recordEvent("person_detected", "line1\nline2")

		body := scrape().Body.String()
		for _, want := range []string{
			`person_detected{camera="say \"hi\""} 1`,
			`person_detected{camera="C:\\cam"} 1`,
			`person_detected{camera="line1\nline2"} 1`,
		} {
			if !strings.Contains(body, want+"\n") {
				t.Errorf("body missing %s\nbody:\n%s", want, body)
			}
		}
	})

	t.Run("empties the cache", func(t *testing.T) {
		resetState(t)
		recordEvent("person_detected", "front door")

		if scrape().Body.Len() == 0 {
			t.Fatal("first scrape: want metrics, got empty body")
		}
		rec := scrape()
		if rec.Code != http.StatusOK {
			t.Errorf("second scrape: got %d, want %d", rec.Code, http.StatusOK)
		}
		if body := rec.Body.String(); body != "" {
			t.Errorf("second scrape: want empty body, got:\n%s", body)
		}
	})
}

// TestConcurrentPostAndScrape checks no events are lost or double counted when
// posts and scrapes overlap. Run with -race to also check for data races.
func TestConcurrentPostAndScrape(t *testing.T) {
	resetState(t)

	const posters, postsEach = 8, 200
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		scraped int64
		done    = make(chan struct{})
	)

	countScrape := func() {
		body := scrape().Body.String()
		var n int64
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "person_detected{") {
				var v int64
				fmt.Sscanf(line[strings.LastIndex(line, " ")+1:], "%d", &v)
				n += v
			}
		}
		mu.Lock()
		scraped += n
		mu.Unlock()
	}

	scraperDone := make(chan struct{})
	go func() {
		defer close(scraperDone)
		for {
			select {
			case <-done:
				return
			default:
				countScrape()
			}
		}
	}()

	for range posters {
		wg.Go(func() {
			for range postsEach {
				if rec := post(event("person_detected", "camera_1")); rec.Code != http.StatusAccepted {
					t.Errorf("post: got %d", rec.Code)
					return
				}
			}
		})
	}
	wg.Wait()
	close(done)
	<-scraperDone
	countScrape() // collect anything posted after the scraper's last pass

	if want := int64(posters * postsEach); scraped != want {
		t.Errorf("scraped %d events in total, want %d", scraped, want)
	}
}

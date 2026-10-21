package main

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Seeded state, created through the API like a real user would: project
// big has 3 environments of 20 services (60), with shared and per-service
// variables that reference each other, domains in production, and
// seedReleases releases of production's svc-00.
// Nine small projects of 3 services each carry an audit log of about
// seedAudit events.
const (
	seedServices = 20
	seedReleases = 50
	seedAudit    = 1000
)

var seedEnvs = []string{"production", "staging", "dev"}

type release struct {
	Number int64  `json:"number"`
	Status string `json:"status"`
}

func (d *daemon) seed() error {
	var deploys []string // release paths to wait for
	deploy := func(svcPath string, body map[string]string) error {
		if body == nil {
			body = map[string]string{}
		}
		var r release
		if err := d.call(http.MethodPost, svcPath+"/releases", body, &r); err != nil {
			return err
		}
		deploys = append(deploys, fmt.Sprintf("%s/releases/%d", svcPath, r.Number))
		return nil
	}

	if err := d.call(http.MethodPost, "/projects", map[string]string{"name": "big"}, nil); err != nil {
		return err
	}
	for _, env := range seedEnvs {
		if env != "production" {
			if err := d.call(http.MethodPost, "/projects/big/environments", map[string]string{"name": env}, nil); err != nil {
				return err
			}
		}
		ep := "/projects/big/environments/" + env
		if err := d.call(http.MethodPatch, ep+"/variables", map[string]any{"set": map[string]string{
			"LOG_LEVEL": "info", "REGION": "eu-north", "FEATURES": "a,b,c",
			"API_URL": "http://${{svc-01.DOCK_PRIVATE_DOMAIN}}:${{svc-01.PORT}}",
		}}, nil); err != nil {
			return err
		}
		for i := range seedServices {
			name := fmt.Sprintf("svc-%02d", i)
			if err := d.call(http.MethodPost, ep+"/services", map[string]any{"name": name, "image": "app:1", "port": 8080}, nil); err != nil {
				return err
			}
			if err := d.call(http.MethodPatch, ep+"/variables", map[string]any{"service": name, "set": map[string]string{
				"DATABASE_URL": "postgres://app:secret@db:5432/" + name,
				"SECRET_KEY":   "0123456789abcdef0123456789abcdef",
				"UPSTREAM":     "${{svc-00.DOCK_PRIVATE_DOMAIN}} ${{shared.REGION}}",
			}}, nil); err != nil {
				return err
			}
			if env == "production" {
				if err := d.call(http.MethodPost, ep+"/services/"+name+"/domains", map[string]string{"hostname": name + ".big.example.com"}, nil); err != nil {
					return err
				}
			}
		}
		if err := d.call(http.MethodPost, ep+"/changes/apply", nil, nil); err != nil {
			return err
		}
		for i := range seedServices {
			if err := deploy(fmt.Sprintf("%s/services/svc-%02d", ep, i), nil); err != nil {
				return err
			}
		}
	}
	for p := 1; p <= 9; p++ {
		proj := fmt.Sprintf("proj-%02d", p)
		if err := d.call(http.MethodPost, "/projects", map[string]string{"name": proj}, nil); err != nil {
			return err
		}
		for s := range 3 {
			ep := "/projects/" + proj + "/environments/production"
			name := fmt.Sprintf("svc-%d", s)
			if err := d.call(http.MethodPost, ep+"/services", map[string]any{"name": name, "image": "app:1", "port": 8080}, nil); err != nil {
				return err
			}
			if err := deploy(ep+"/services/"+name, nil); err != nil {
				return err
			}
		}
	}
	if err := d.waitAll(deploys); err != nil {
		return err
	}

	svc := "/projects/big/environments/production/services/svc-00"
	for i := 2; i <= seedReleases; i++ {
		deploys = deploys[:0]
		if err := deploy(svc, map[string]string{"image": fmt.Sprintf("app:%d", i)}); err != nil {
			return err
		}
		if err := d.waitAll(deploys); err != nil {
			return err
		}
	}

	for i := range seedAudit {
		ep := fmt.Sprintf("/projects/proj-%02d/environments/production", i%9+1)
		if err := d.call(http.MethodPatch, ep+"/variables", map[string]any{"set": map[string]string{"PAD": fmt.Sprint(i)}}, nil); err != nil {
			return err
		}
	}
	return nil
}

// waitAll polls until every release in paths has finished deploying.
func (d *daemon) waitAll(paths []string) error {
	deadline := time.Now().Add(60 * time.Second)
	for _, p := range paths {
		for {
			var r release
			if err := d.call(http.MethodGet, p, nil, &r); err != nil {
				return err
			}
			if r.Status == "active" || r.Status == "failed" || r.Status == "superseded" {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("release %s did not finish: %s", p, r.Status)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return nil
}

const (
	latencyConcurrency = 8
	latencyRequests    = 1000
	latencyWarmup      = 50
)

// latency measures the main reads the UI and CLI make, at
// latencyConcurrency concurrent clients, against apiP95Budget times speed
// (see options.speed). The project overview and the
// pending-changes diff are measured again with a staged change, which makes
// them resolve every service's variables.
func (d *daemon) latency(t *table, speed float64) error {
	budget := time.Duration(float64(apiP95Budget) * speed)
	budgetText := "≤ " + ms(budget)
	if speed > 1 {
		budgetText += fmt.Sprintf(" (%s × %.2f machine speed)", ms(apiP95Budget), speed)
	}
	prod := "/projects/big/environments/production"
	svc := prod + "/services/svc-00"
	measure := func(suffix string, paths ...string) error {
		for _, path := range paths {
			p50, p95, err := d.load(path)
			if err != nil {
				return err
			}
			t.within("API p95 GET "+path+suffix, fmt.Sprintf("%s (p50 %s)", ms(p95), ms(p50)), budgetText, p95 <= budget)
		}
		return nil
	}
	if err := measure("",
		"/me",
		"/projects",
		"/projects/big/overview",
		"/projects/big/environments",
		prod+"/services",
		svc,
		svc+"/releases",
		svc+"/releases?limit=100",
		fmt.Sprintf("%s/releases/%d", svc, seedReleases/2),
		prod+"/variables",
		svc+"/domains",
		"/audit",
		"/audit?project=proj-01",
		"/audit?project=big&environment=production&service=svc-00",
		"/servers",
	); err != nil {
		return err
	}
	if err := d.call(http.MethodPatch, prod+"/variables", map[string]any{"set": map[string]string{"REGION": "eu-west"}}, nil); err != nil {
		return err
	}
	return measure(" (staged change)", "/projects/big/overview", prod+"/changes")
}

// load sends latencyRequests GETs to path from latencyConcurrency workers
// and returns the median and 95th percentile latency.
func (d *daemon) load(path string) (p50, p95 time.Duration, err error) {
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: latencyConcurrency}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	get := func() (time.Duration, error) {
		req, _ := http.NewRequest(http.MethodGet, d.base+path, nil)
		req.Header.Set("Authorization", "Bearer "+d.token)
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("GET %s: %d", path, resp.StatusCode)
		}
		return time.Since(start), err
	}
	for range latencyWarmup {
		if _, err := get(); err != nil {
			return 0, 0, err
		}
	}
	var (
		mu    sync.Mutex
		times = make([]time.Duration, 0, latencyRequests)
		first error
		wg    sync.WaitGroup
		next  = make(chan struct{}, latencyRequests)
	)
	for range latencyRequests {
		next <- struct{}{}
	}
	close(next)
	for range latencyConcurrency {
		wg.Go(func() {
			for range next {
				dur, err := get()
				mu.Lock()
				times = append(times, dur)
				if err != nil && first == nil {
					first = err
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if first != nil {
		return 0, 0, first
	}
	slices.Sort(times)
	return times[len(times)/2], times[len(times)*95/100], nil
}

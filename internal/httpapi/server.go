package httpapi

import (
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/model"
	"github.com/vooon/pathosd/internal/policy"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type ServerDeps struct {
	Config  *config.Config
	Metrics *metrics.Metrics
	BGP     *bgp.Manager
	Policy  *policy.Manager
	// Schedulers holds one scheduler per check, keyed by VIP name.
	Schedulers map[string][]*checks.Scheduler
}

type landingPageData struct {
	RouterID    string
	ASN         uint32
	Version     string
	Commit      string
	GeneratedAt time.Time
	Peers       []model.PeerStatus
	VIPs        []model.VIPStatus
}

//go:embed landing/*
var landingFS embed.FS

var landingPageTmpl = template.Must(template.ParseFS(landingFS, "landing/index.html"))

var landingStaticFS = mustSubFS(landingFS, "landing")

func mustSubFS(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func NewServer(deps ServerDeps) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleLanding(deps))
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(landingStaticFS))))
	mux.HandleFunc("GET /healthz", handleHealthz())
	mux.HandleFunc("GET /readyz", handleReadyz(deps.BGP, deps.Config))
	mux.HandleFunc("GET /status", handleStatus(deps))
	mux.Handle("GET /metrics", promhttp.HandlerFor(deps.Metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /api/v1/vips/{name}/check", handleTriggerCheck(deps.Policy, deps.Schedulers))
	api := deps.Config.API
	return &http.Server{
		Addr:              api.Listen,
		Handler:           otelhttp.NewHandler(mux, "pathosd.http"),
		ReadHeaderTimeout: durationOr(api.ReadHeaderTimeout, 10*time.Second),
		ReadTimeout:       durationOr(api.ReadTimeout, 30*time.Second),
		WriteTimeout:      durationOr(api.WriteTimeout, 60*time.Second),
		IdleTimeout:       durationOr(api.IdleTimeout, 120*time.Second),
	}
}

// durationOr returns d when set (config defaults normally fill it in) and
// fallback otherwise, so NewServer stays safe on a Config built by hand.
func durationOr(d *config.Duration, fallback time.Duration) time.Duration {
	if d == nil {
		return fallback
	}
	return d.Duration
}

func handleHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleLanding(deps ServerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := landingPageData{
			RouterID:    deps.Config.Router.RouterID,
			ASN:         deps.Config.Router.ASN,
			Version:     version.Version,
			Commit:      version.GetRevision(),
			GeneratedAt: time.Now(),
			Peers:       deps.BGP.GetPeerStates(r.Context()),
			VIPs:        vipStatuses(deps),
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := landingPageTmpl.Execute(w, data); err != nil {
			slog.Error("failed to render landing page", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}
}

func handleReadyz(bgpMgr *bgp.Manager, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peers := bgpMgr.GetPeerStates(r.Context())
		allReady := true
		var unready []model.PeerStatus
		for _, p := range peers {
			if !p.Required {
				continue
			}
			if p.SessionState != "established" {
				allReady = false
				unready = append(unready, p)
			}
		}
		if allReady {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "peers": peers})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "unready": unready, "peers": peers})
		}
	}
}

func handleStatus(deps ServerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := model.DaemonStatus{
			RouterID: deps.Config.Router.RouterID,
			ASN:      deps.Config.Router.ASN,
			Version:  version.Version,
			Commit:   version.GetRevision(),
			Peers:    deps.BGP.GetPeerStates(r.Context()),
			VIPs:     vipStatuses(deps),
		}
		writeJSON(w, http.StatusOK, status)
	}
}

// vipStatuses returns policy VIP statuses enriched with scheduler counters.
// vipStatuses returns policy VIP statuses enriched with scheduler counters:
// per check, and at VIP level the lowest consecutive_ok / highest
// consecutive_fail across the VIP's checks.
func vipStatuses(deps ServerDeps) []model.VIPStatus {
	statuses := deps.Policy.GetVIPStatuses()
	for i := range statuses {
		st := &statuses[i]
		byName := make(map[string]*checks.Scheduler)
		for _, sched := range deps.Schedulers[st.Name] {
			byName[sched.CheckName()] = sched
		}
		first := true
		for j := range st.Checks {
			sched, ok := byName[st.Checks[j].Name]
			if !ok {
				continue
			}
			okN, failN := sched.ConsecutiveOK(), sched.ConsecutiveFail()
			st.Checks[j].ConsecutiveOK = okN
			st.Checks[j].ConsecutiveFail = failN
			if first || okN < st.ConsecutiveOK {
				st.ConsecutiveOK = okN
			}
			if first || failN > st.ConsecutiveFail {
				st.ConsecutiveFail = failN
			}
			first = false
		}
	}
	return statuses
}

// handleTriggerCheck runs every check of the VIP once (or only ?check=<name>)
// and returns the per-check results plus a combined result that follows the
// VIP's checks_policy. With a single check "result" is that check's result.
func handleTriggerCheck(pol *policy.Manager, schedulers map[string][]*checks.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		scheds, ok := schedulers[name]
		if !ok || len(scheds) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "VIP not found: " + name})
			return
		}
		if only := r.URL.Query().Get("check"); only != "" {
			var selected []*checks.Scheduler
			for _, s := range scheds {
				if s.CheckName() == only {
					selected = append(selected, s)
				}
			}
			if len(selected) == 0 {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "check not found: " + only})
				return
			}
			scheds = selected
		}
		slog.Info("ad-hoc check triggered", "vip", name, "remote_addr", r.RemoteAddr)
		results := make(map[string]checks.Result, len(scheds))
		healthy := make(map[string]bool, len(scheds))
		var combined checks.Result
		var details []string
		for i, sched := range scheds {
			result, err := sched.TriggerCheck(r.Context())
			if err != nil {
				slog.Error("ad-hoc check failed", "vip", name, "check", sched.CheckName(), "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			results[sched.CheckName()] = result
			healthy[sched.CheckName()] = result.Success
			if i == 0 {
				combined = result
			} else {
				combined.Duration += result.Duration
			}
			if result.Detail != "" {
				details = append(details, sched.CheckName()+": "+result.Detail)
			}
		}
		if len(schedulers[name]) > 1 {
			// Partial runs (?check=) still combine through the policy; the
			// checks not run count as failed.
			combined = checks.Result{Duration: combined.Duration, Detail: strings.Join(details, "; ")}
			combined.Success, _ = pol.CombineChecks(name, healthy)
		}
		writeJSON(w, http.StatusOK, map[string]any{"vip": name, "result": combined, "results": results})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to write JSON response", "status", status, "error", err)
	}
}

package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/model"
	"github.com/vooon/pathosd/internal/policy"
)

type ServerDeps struct {
	Config     *config.Config
	Metrics    *metrics.Metrics
	BGP        *bgp.Manager
	Policy     *policy.Manager
	Schedulers map[string]*checks.Scheduler
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
	mux.HandleFunc("POST /api/v1/vips/{name}/check", handleTriggerCheck(deps.Schedulers))
	return &http.Server{Addr: deps.Config.API.Listen, Handler: mux}
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
			VIPs:        deps.Policy.GetVIPStatuses(),
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
			writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ready", "peers": peers})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"status": "not_ready", "unready": unready, "peers": peers})
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
			VIPs:     deps.Policy.GetVIPStatuses(),
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func handleTriggerCheck(schedulers map[string]*checks.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		sched, ok := schedulers[name]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "VIP not found: " + name})
			return
		}
		result, err := sched.TriggerCheck(r.Context())
		if err != nil {
			slog.Error("ad-hoc check failed", "vip", name, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"vip": name, "result": result})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to write JSON response", "status", status, "error", err)
	}
}

func ListenAndServe(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP API listening", "address", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		return srv.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}

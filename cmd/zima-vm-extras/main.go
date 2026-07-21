// zima-vm-extras: a community add-on for ZimaOS's built-in ZVM module
// providing autostart, snapshots and USB passthrough features that the
// official UI lacks.
//
// The daemon binds to 127.0.0.1 only and registers a reverse-proxy route
// with the ZimaOS gateway, so the Web UI reaches it same-origin via port 80
// — the same pattern the zima_cron module uses. The bind is loopback-only,
// but the gateway route makes the daemon reachable from the LAN over port 80;
// state-changing requests are therefore guarded by an Origin-header CSRF check.
package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chicohaager/zima-vm-extras/internal/autostart"
	"github.com/chicohaager/zima-vm-extras/internal/backup"
	"github.com/chicohaager/zima-vm-extras/internal/config"
	"github.com/chicohaager/zima-vm-extras/internal/gateway"
	"github.com/chicohaager/zima-vm-extras/internal/handlers"
	"github.com/chicohaager/zima-vm-extras/internal/mounts"
	"github.com/chicohaager/zima-vm-extras/internal/pci"
	"github.com/chicohaager/zima-vm-extras/internal/schedule"
	"github.com/chicohaager/zima-vm-extras/internal/usb"
	"github.com/chicohaager/zima-vm-extras/internal/virsh"
	"github.com/chicohaager/zima-vm-extras/internal/vnc"
	"github.com/chicohaager/zima-vm-extras/internal/watchdog"
)

func main() {
	cfg := config.Load()
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("zima-vm-extras starting: bind=%s:%s data_dir=%s route=%s",
		cfg.BindAddr, cfg.Port, cfg.DataDir, cfg.RoutePath)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("mkdir data_dir: %v", err)
	}

	store, err := autostart.New(filepath.Join(cfg.DataDir, "autostart.json"))
	if err != nil {
		log.Fatalf("open autostart store: %v", err)
	}

	vc := virsh.New(cfg.VirshBin)
	snapRoot := filepath.Join(cfg.DataDir, "snapshots")

	mountMgr, err := mounts.New(filepath.Join(cfg.DataDir, "mounts.json"))
	if err != nil {
		log.Fatalf("open mounts store: %v", err)
	}

	usbStore, err := usb.NewStore(filepath.Join(cfg.DataDir, "usb.json"))
	if err != nil {
		log.Fatalf("open usb store: %v", err)
	}

	pciStore, err := pci.NewStore(filepath.Join(cfg.DataDir, "pci.json"))
	if err != nil {
		log.Fatalf("open pci store: %v", err)
	}

	schedStore, err := schedule.NewStore(filepath.Join(cfg.DataDir, "schedule.json"))
	if err != nil {
		log.Fatalf("open schedule store: %v", err)
	}

	vncStore, err := vnc.NewStore(filepath.Join(cfg.DataDir, "vnc.json"))
	if err != nil {
		log.Fatalf("open vnc store: %v", err)
	}

	backupMgr := backup.NewManager(vc, log.Printf)

	// Re-mount everything tagged AutoMount=true. Best effort; errors are logged.
	go mountMgr.MountAllAuto(log.Printf)

	// Orchestrate boot autostart: start enabled VMs in Order, with delays.
	// Runs once per boot (guarded by a tmpfs marker).
	go store.Run(vc, log.Printf)

	srv := handlers.NewServer(vc, store, mountMgr, usbStore, pciStore, schedStore, backupMgr, vncStore, snapRoot)

	mux := http.NewServeMux()
	mux.Handle("/api/", srv.Routes())

	// Static UI for any other path. The gateway also serves these files
	// directly under /modules/zima_vm_extras/; this is the fallback for
	// direct localhost access on the daemon port.
	fs := http.FileServer(http.Dir(cfg.StaticDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fs.ServeHTTP(w, r)
	})

	// The ZimaOS gateway proxies the registered route prefix verbatim
	// (e.g. /v2/vm_extras/api/health). Strip it so internal routing stays
	// prefix-agnostic and direct localhost access (/api/...) keeps working.
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cap request bodies — no API payload is anywhere near 1 MiB.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

		// CSRF: reject cross-origin state-changing requests. Browsers always
		// send Origin on POST/PUT/DELETE; for the same-origin UI it equals the
		// Host. Non-browser clients (no Origin header) are unaffected.
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request rejected", http.StatusForbidden)
					return
				}
			}
		}

		// Strip the gateway route prefix, but only at a path boundary: the
		// prefix must equal the whole path or be followed by a "/", so an
		// unrelated path that merely starts with the prefix string is left
		// untouched.
		if cfg.RoutePath != "" {
			if r.URL.Path == cfg.RoutePath {
				r.URL.Path = "/"
			} else if strings.HasPrefix(r.URL.Path, cfg.RoutePath+"/") {
				r.URL.Path = r.URL.Path[len(cfg.RoutePath):]
			}
		}
		mux.ServeHTTP(w, r)
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Register the reverse-proxy route with the ZimaOS gateway so the UI can
	// reach this daemon same-origin via port 80. Retries until the gateway
	// is reachable.
	gw := gateway.New(cfg.GatewayURLFile, cfg.RoutePath, "http://127.0.0.1:"+cfg.Port)
	go gw.RegisterWithRetry(ctx, log.Printf)

	// Install the boot watchdog timer on the persistent root so the service
	// survives reboots even if the in-sysext unit loses the multi-user.target
	// race (see ZimaOS sysext boot-order pitfall).
	go func() {
		if err := watchdog.EnsureInstalled(log.Printf); err != nil {
			log.Printf("watchdog setup (non-fatal): %v", err)
		}
	}()

	// Keep pinned USB and PCI passthroughs alive: periodically re-add any
	// that the official ZVM UI stripped from a domain's persistent config.
	go usbStore.RunReconciler(ctx, vc, 60*time.Second, log.Printf)
	go pciStore.RunReconciler(ctx, vc, 60*time.Second, log.Printf)

	// Keep pinned VNC console passwords applied: re-set any the ZVM UI
	// stripped, so a VM's console never falls back to open LAN access.
	go vncStore.RunReconciler(ctx, vc, 60*time.Second, log.Printf)

	// Keep watchdog-enabled VMs running.
	go store.RunWatchdog(ctx, vc, 30*time.Second, log.Printf)

	// Run periodic, retention-limited snapshots.
	go schedStore.Run(ctx, vc, snapRoot, 10*time.Minute, log.Printf)

	httpSrv := &http.Server{
		Addr:              cfg.BindAddr + ":" + cfg.Port,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutdown signal received, stopping")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
}

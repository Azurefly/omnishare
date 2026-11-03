package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"omnishare/internal/api"
	"omnishare/internal/config"
	"omnishare/internal/desktop"
	"omnishare/internal/discovery"
	"omnishare/internal/instance"
	"omnishare/internal/storage"
)

func main() {
	port := flag.Int("port", 0, "HTTP port override")
	dataDir := flag.String("data-dir", defaultDataDir(), "data directory")
	name := flag.String("name", "", "node name override")
	listenAddress := flag.String("listen", "", "listen address override; LAN addresses require an access key")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file")
	tlsKey := flag.String("tls-key", "", "TLS private key file")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser automatically")
	storageEngine := flag.String("storage-engine", defaultStorageEngine(), "storage engine: sqlite (default) or json")
	migrateStorage := flag.Bool("migrate-storage", false, "migrate omnishare.json to SQLite and exit")
	flag.Parse()

	lock, err := instance.Acquire(*dataDir)
	if err != nil {
		log.Fatalf("instance lock: %v", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			log.Printf("instance lock release: %v", err)
		}
	}()

	cfg, err := config.New(*dataDir, *port, *name)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *listenAddress != "" || *tlsCert != "" || *tlsKey != "" {
		if err := cfg.ApplyRuntimeOverrides(*listenAddress, *tlsCert, *tlsKey); err != nil {
			log.Fatalf("runtime overrides: %v", err)
		}
	}
	current := cfg.Get()
	if *migrateStorage {
		report, err := migrateStorageNow(current.DataDir)
		if err != nil {
			log.Fatalf("migrate storage: %v", err)
		}
		log.Printf("migration report: %+v", report)
		return
	}
	store, storageState, err := openStorage(current.DataDir, *storageEngine)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("storage close: %v", err)
		}
	}()

	addr := net.JoinHostPort(current.ListenAddress, fmt.Sprintf("%d", current.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	defer listener.Close()

	nodeID, discoveryKey, err := cfg.DiscoveryIdentity()
	if err != nil {
		log.Fatalf("discovery identity: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := discovery.NewRegistryWithIdentity(nodeID, discoveryKey)
	// Discovery starts only after the HTTP listener is known to be available.
	if current.AllowLAN {
		registry.Start(ctx, current.NodeName, current.Port)
	}

	server := api.New(store, cfg, registry)
	server.SetStorageState(storageState)
	if tokenPath := server.InitTokenPath(); tokenPath != "" {
		log.Printf("first-run setup: writing config requires header X-OmniShare-Init-Token; the one-time token is in %s (mode 0600, delete after setting an access key)", tokenPath)
	}
	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Minute,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		cleanupLoop(ctx, store, cfg)
	}()

	serveErr := make(chan error, 1)
	go func() {
		if current.TLSCertFile != "" {
			serveErr <- httpServer.ServeTLS(listener, current.TLSCertFile, current.TLSKeyFile)
			return
		}
		serveErr <- httpServer.Serve(listener)
	}()

	scheme := "http"
	if current.TLSCertFile != "" {
		scheme = "https"
	}
	localURL := fmt.Sprintf("%s://%s", scheme, net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", current.Port)))
	log.Printf("OmniShare v%s started: %s", api.Version, localURL)
	log.Printf("Listen: %s | Data: %s | LAN: %t | TLS: %t", addr, current.DataDir, current.AllowLAN, current.TLSCertFile != "")
	if !*noBrowser && current.AutoOpenBrowser {
		go func() {
			time.Sleep(350 * time.Millisecond)
			if err := desktop.OpenBrowser(localURL); err != nil {
				log.Printf("browser: %v", err)
			}
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-quit:
		log.Printf("received %s, shutting down", sig)
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server stopped unexpectedly: %v", err)
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		log.Printf("cleanup worker did not stop within deadline")
	}
	server.FlushDeniedAudits()
}

// openStorage selects the production SQLite engine. If the one-shot JSON
// import fails, the process keeps running on the legacy JSON engine and reports
// "not migrated" through the health endpoint instead of failing to boot.
func openStorage(dataDir, engine string) (storage.Backend, api.StorageState, error) {
	if engine == "json" {
		store, err := storage.New(dataDir)
		if err != nil {
			return nil, api.StorageState{}, err
		}
		return store, api.StorageState{Engine: "json"}, nil
	}
	store, err := storage.NewSQLite(dataDir)
	if err == nil {
		report := store.MigrationInfo()
		return store, api.StorageState{Engine: "sqlite", MigrationPending: !report.Migrated}, nil
	}
	jsonPath := filepath.Join(dataDir, "omnishare.json")
	if _, statErr := os.Stat(jsonPath); statErr != nil {
		return nil, api.StorageState{}, err
	}
	log.Printf("sqlite unavailable, falling back to JSON (not migrated): %v", err)
	legacy, legacyErr := storage.New(dataDir)
	if legacyErr != nil {
		return nil, api.StorageState{}, fmt.Errorf("sqlite: %v; json fallback: %w", err, legacyErr)
	}
	return legacy, api.StorageState{Engine: "json", MigrationFailed: true, MigrationError: err.Error()}, nil
}

func migrateStorageNow(dataDir string) (storage.MigrationReport, error) {
	store, err := storage.NewSQLite(dataDir)
	if err != nil {
		return storage.MigrationReport{}, err
	}
	defer store.Close()
	return store.MigrationInfo(), nil
}

func defaultStorageEngine() string {
	if value := os.Getenv("OMNISHARE_STORAGE"); value != "" {
		return value
	}
	return "sqlite"
}

func cleanupLoop(ctx context.Context, store storage.Backend, cfg *config.Manager) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer store.FlushStats()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := cfg.Get()
			if _, err := store.CleanupExpired(current.RetentionDays); err != nil {
				log.Printf("cleanup expired: %v", err)
			}
			if _, err := store.PruneAudits(current.AuditRetentionDays, current.AuditMaxEvents); err != nil {
				log.Printf("prune audits: %v", err)
			}
			files, _, err := store.PurgeExpiredTrash(current.TrashRetentionDays)
			if err != nil {
				log.Printf("purge expired trash: %v", err)
				continue
			}
			for _, f := range files {
				if store.StorageReferencedByOthers(f.StoragePath, f.ID) {
					continue
				}
				if err := os.Remove(filepath.Join(current.DataDir, f.StoragePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
					log.Printf("purge file %s: %v", f.ID, err)
					_ = store.AddAudit("purge_pending", "file", f.ID, "automatic physical deletion failed", map[string]interface{}{"error": err.Error()})
				}
			}
		}
	}
}

func defaultDataDir() string {
	if value := os.Getenv("OMNISHARE_DATA_DIR"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./data"
	}
	return filepath.Join(home, ".omnishare")
}

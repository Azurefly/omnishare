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
	store, err := storage.New(current.DataDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

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
}

func cleanupLoop(ctx context.Context, store *storage.Store, cfg *config.Manager) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := cfg.Get()
			if _, err := store.CleanupExpired(current.RetentionDays); err != nil {
				log.Printf("cleanup expired: %v", err)
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

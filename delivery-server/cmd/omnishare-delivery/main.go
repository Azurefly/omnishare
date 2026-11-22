package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	delivery "omnishare-delivery"
)

func loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && net.ParseIP(host).IsLoopback()
}

func main() {
	root := flag.String("data-dir", "", "private delivery data directory")
	origin := flag.String("public-origin", "", "HTTPS origin visible to visitors")
	publicAddress := flag.String("public-listen", "127.0.0.1:18484", "public handler for an HTTPS reverse proxy")
	adminAddress := flag.String("admin-listen", "127.0.0.1:18485", "management handler for a private route")
	publishersPath := flag.String("publishers", "", "JSON file mapping publisher names to SHA-256 credential digests")
	maxBytes := flag.Int64("max-bytes", 16<<20, "maximum bytes per bundle")
	capacity := flag.Int64("capacity", 32<<20, "maximum total bytes")
	reserve := flag.Int64("disk-reserve", 256<<20, "minimum free bytes to leave on the filesystem")
	flag.Parse()
	if !loopback(*publicAddress) || !loopback(*adminAddress) || *publicAddress == *adminAddress || *reserve < 0 {
		log.Fatal("public and management handlers must use distinct loopback addresses")
	}
	var publishers map[string]string
	raw, err := os.ReadFile(*publishersPath)
	if err != nil || json.Unmarshal(raw, &publishers) != nil {
		log.Fatal("invalid publisher credential file")
	}
	service, err := delivery.New(delivery.Options{Root: *root, PublicOrigin: *origin, Publishers: publishers,
		MaxBytes: *maxBytes, CapacityBytes: *capacity,
		CheckSpace: func(size int64) bool {
			free, err := freeBytes(*root)
			return err == nil && free >= *reserve && size <= free-*reserve
		}})
	if err != nil {
		log.Fatal(err)
	}
	admin, err := net.Listen("tcp", *adminAddress)
	if err != nil {
		log.Fatal(err)
	}
	public, err := net.Listen("tcp", *publicAddress)
	if err != nil {
		_ = admin.Close()
		log.Fatal(err)
	}
	servers := []*http.Server{
		{Handler: service.Public(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 45 * time.Second, MaxHeaderBytes: 16 << 10},
		{Handler: service.Admin(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10},
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result := make(chan error, 2)
	go func() { result <- servers[0].Serve(public) }()
	go func() { result <- servers[1].Serve(admin) }()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	log.Print("OmniShare delivery ready")
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err := <-result:
			if !errors.Is(err, http.ErrServerClosed) {
				log.Print("listener stopped: ", err)
			}
			break loop
		case <-ticker.C:
			service.Sweep()
		}
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
	}
}

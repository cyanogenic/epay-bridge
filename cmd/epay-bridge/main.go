package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"epay-bridge/internal/config"
	"epay-bridge/internal/provider"
	"epay-bridge/internal/service"
	"epay-bridge/internal/store"
	"epay-bridge/internal/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	paymentProviders := make([]provider.Provider, 0, 2)
	enabledProviders := make([]string, 0, 2)
	if cfg.Alipay.Enabled {
		alipayProvider, err := provider.NewAlipay(cfg.Alipay, cfg.PublicBaseURL)
		if err != nil {
			slog.Error("initialize Alipay", "error", err)
			os.Exit(1)
		}
		paymentProviders = append(paymentProviders, alipayProvider)
		enabledProviders = append(enabledProviders, alipayProvider.Name())
	}
	if cfg.WeChat.Enabled {
		wechatProvider, err := provider.NewWeChat(cfg.WeChat, cfg.PublicBaseURL)
		if err != nil {
			slog.Error("initialize WeChat Pay", "error", err)
			os.Exit(1)
		}
		paymentProviders = append(paymentProviders, wechatProvider)
		enabledProviders = append(enabledProviders, wechatProvider.Name())
	}
	slog.Info("payment providers enabled", "providers", enabledProviders)

	payments := service.NewPayments(cfg, database, paymentProviders...)
	handler, err := web.NewServer(cfg, database, payments)
	if err != nil {
		slog.Error("initialize HTTP server", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr: cfg.ListenAddress, Handler: handler.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 << 10,
	}

	go payments.RunNotifier(ctx)
	go payments.RunReconciler(ctx)
	go func() {
		slog.Info("EPay Bridge listening", "address", cfg.ListenAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP shutdown failed", "error", err)
	}
}

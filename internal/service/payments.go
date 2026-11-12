package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"epay-bridge/internal/config"
	"epay-bridge/internal/domain"
	"epay-bridge/internal/epay"
	"epay-bridge/internal/provider"
	"epay-bridge/internal/store"
)

type Payments struct {
	cfg       config.Config
	store     *store.Store
	providers map[string]provider.Provider
	client    *http.Client
	createMu  sync.Mutex
}

func NewPayments(cfg config.Config, store *store.Store, providers ...provider.Provider) *Payments {
	registry := make(map[string]provider.Provider, len(providers))
	for _, item := range providers {
		registry[item.Name()] = item
	}
	return &Payments{
		cfg: cfg, store: store, providers: registry,
		client: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (p *Payments) Create(ctx context.Context, req *epay.PurchaseRequest) (*domain.Order, provider.CheckoutKind, error) {
	selected, ok := p.providers[req.Type]
	if !ok {
		return nil, "", errors.New("payment provider is not configured")
	}
	// This gateway is deliberately single-process/single-merchant. Serializing
	// provider order creation prevents duplicate upstream creates for concurrent
	// retries of the same EPay order number.
	p.createMu.Lock()
	defer p.createMu.Unlock()

	order, _, err := p.store.CreateOrGetOrder(ctx, domain.CreateOrder{
		MerchantOrderNo: req.MerchantOrderNo, Channel: req.Type, Name: req.Name,
		AmountCents: req.AmountCents, NotifyURL: req.NotifyURL, ReturnURL: req.ReturnURL,
		ExpiresAt: time.Now().UTC().Add(p.cfg.OrderTTL),
	})
	if err != nil {
		return nil, "", err
	}
	if order.ProviderPayURL != "" {
		return order, checkoutKind(order.Channel), nil
	}
	if order.Status != domain.StatusPending {
		return order, checkoutKind(order.Channel), nil
	}
	checkout, err := selected.Create(ctx, *order)
	if err != nil {
		return nil, "", fmt.Errorf("create provider order: %w", err)
	}
	if err := p.store.SetProviderPayURL(ctx, order.ID, checkout.URL); err != nil {
		return nil, "", err
	}
	order.ProviderPayURL = checkout.URL
	return order, checkout.Kind, nil
}

func (p *Payments) ProcessNotify(ctx context.Context, channel string, req *http.Request) (*domain.Order, error) {
	selected, ok := p.providers[channel]
	if !ok {
		return nil, errors.New("unknown payment provider")
	}
	event, err := selected.ParseNotify(req)
	if err != nil {
		return nil, err
	}
	order, err := p.store.GetOrderByMerchantNo(ctx, event.MerchantOrderNo)
	if err != nil {
		return nil, err
	}
	if err := validateEvent(*order, channel, event); err != nil {
		return nil, err
	}
	if event.State == provider.PaymentPaid {
		_, err = p.store.MarkPaid(ctx, domain.PaymentConfirmation{
			OrderID: order.ID, ProviderTransactionID: event.ProviderTransactionID,
			ProviderPayload: event.Payload, PaidAt: event.PaidAt,
		})
		if err != nil {
			return nil, err
		}
		order, err = p.store.GetOrderByID(ctx, order.ID)
		return order, err
	}
	if event.State == provider.PaymentClosed {
		if err := p.store.MarkClosed(ctx, order.ID); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func (p *Payments) ReconcileOrder(ctx context.Context, order domain.Order) error {
	selected, ok := p.providers[order.Channel]
	if !ok {
		return errors.New("provider not configured")
	}
	event, err := selected.Query(ctx, order)
	if err != nil {
		return err
	}
	if err := validateEvent(order, order.Channel, event); err != nil {
		return err
	}
	switch event.State {
	case provider.PaymentPaid:
		_, err = p.store.MarkPaid(ctx, domain.PaymentConfirmation{
			OrderID: order.ID, ProviderTransactionID: event.ProviderTransactionID,
			ProviderPayload: event.Payload, PaidAt: event.PaidAt,
		})
	case provider.PaymentClosed:
		err = p.store.MarkClosed(ctx, order.ID)
	case provider.PaymentPending:
		if time.Now().UTC().After(order.ExpiresAt) {
			// Close at the provider before changing local state. A blind local
			// expiry can lose a real payment whose provider callback was delayed.
			if closeErr := selected.Close(ctx, order); closeErr != nil {
				return closeErr
			}
			err = p.store.MarkClosed(ctx, order.ID)
		}
	}
	return err
}

func (p *Payments) ReturnURL(order domain.Order) (string, error) {
	status := "WAIT_BUYER_PAY"
	if order.Status == domain.StatusPaid {
		status = "TRADE_SUCCESS"
	} else if order.Status == domain.StatusClosed || order.Status == domain.StatusFailed {
		status = "TRADE_CLOSED"
	}
	params, err := p.epayResult(order, status)
	if err != nil {
		return "", err
	}
	target, err := url.Parse(order.ReturnURL)
	if err != nil {
		return "", err
	}
	query := target.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	target.RawQuery = query.Encode()
	return target.String(), nil
}

func (p *Payments) RunNotifier(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.deliverDue(ctx)
		}
	}
}

func (p *Payments) RunReconciler(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			orders, err := p.store.PendingOrders(ctx, time.Now().UTC().Add(-2*time.Minute), 100)
			if err != nil {
				slog.Error("load pending orders", "error", err)
				continue
			}
			for _, order := range orders {
				if err := p.ReconcileOrder(ctx, order); err != nil {
					slog.Warn("reconcile payment", "order_id", order.ID, "channel", order.Channel, "error", err)
				}
			}
		}
	}
}

func (p *Payments) deliverDue(ctx context.Context) {
	deliveries, err := p.store.DueDeliveries(ctx, 20)
	if err != nil {
		slog.Error("load callback deliveries", "error", err)
		return
	}
	for _, delivery := range deliveries {
		order, err := p.store.GetOrderByID(ctx, delivery.OrderID)
		if err != nil {
			slog.Error("load callback order", "order_id", delivery.OrderID, "error", err)
			continue
		}
		params, err := p.epayResult(*order, "TRADE_SUCCESS")
		if err != nil {
			p.failDelivery(ctx, delivery, 0, err.Error())
			continue
		}
		form := url.Values{}
		for key, value := range params {
			form.Set(key, value)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.NotifyURL, strings.NewReader(form.Encode()))
		if err != nil {
			p.failDelivery(ctx, delivery, 0, err.Error())
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "epay-bridge/1.0")
		resp, err := p.client.Do(req)
		if err != nil {
			p.failDelivery(ctx, delivery, 0, err.Error())
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		if readErr != nil {
			p.failDelivery(ctx, delivery, resp.StatusCode, readErr.Error())
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.TrimSpace(string(body)) == "success" {
			if err := p.store.MarkDeliverySucceeded(ctx, delivery.ID, resp.StatusCode); err != nil {
				slog.Error("mark callback delivered", "delivery_id", delivery.ID, "error", err)
			}
			continue
		}
		p.failDelivery(ctx, delivery, resp.StatusCode, "unexpected callback response")
	}
}

func (p *Payments) failDelivery(ctx context.Context, delivery domain.Delivery, status int, message string) {
	delay := retryDelay(delivery.Attempts + 1)
	if err := p.store.MarkDeliveryFailed(ctx, delivery.ID, status, message, time.Now().UTC().Add(delay)); err != nil {
		slog.Error("record callback failure", "delivery_id", delivery.ID, "error", err)
	}
}

func (p *Payments) epayResult(order domain.Order, tradeStatus string) (map[string]string, error) {
	return epay.SignedCopy(map[string]string{
		"pid": p.cfg.EPayPID, "trade_no": order.ID, "out_trade_no": order.MerchantOrderNo,
		"type": order.Channel, "name": order.Name, "money": epay.FormatCents(order.AmountCents),
		"trade_status": tradeStatus,
	}, p.cfg.EPayKey)
}

func validateEvent(order domain.Order, channel string, event *provider.Event) error {
	if event == nil || event.MerchantOrderNo != order.MerchantOrderNo || order.Channel != channel {
		return errors.New("provider event does not match order")
	}
	if event.AmountCents != order.AmountCents {
		return errors.New("provider amount does not match order")
	}
	if event.State == provider.PaymentPaid && event.ProviderTransactionID == "" {
		return errors.New("paid provider event has no transaction ID")
	}
	return nil
}

func checkoutKind(channel string) provider.CheckoutKind {
	if channel == "wxpay" {
		return provider.CheckoutQRCode
	}
	return provider.CheckoutRedirect
}

func retryDelay(attempt int) time.Duration {
	delays := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}
	if attempt <= 0 {
		return delays[0]
	}
	if attempt > len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempt-1]
}

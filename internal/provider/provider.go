package provider

import (
	"context"
	"net/http"
	"time"

	"epay-bridge/internal/domain"
)

type CheckoutKind string

const (
	CheckoutRedirect CheckoutKind = "redirect"
	CheckoutQRCode   CheckoutKind = "qr_code"
)

type Checkout struct {
	Kind CheckoutKind
	URL  string
}

type PaymentState string

const (
	PaymentPending PaymentState = "pending"
	PaymentPaid    PaymentState = "paid"
	PaymentClosed  PaymentState = "closed"
)

type Event struct {
	MerchantOrderNo       string
	ProviderTransactionID string
	AmountCents           int64
	State                 PaymentState
	PaidAt                time.Time
	Payload               []byte
}

type Provider interface {
	Name() string
	Create(context.Context, domain.Order) (*Checkout, error)
	ParseNotify(*http.Request) (*Event, error)
	Query(context.Context, domain.Order) (*Event, error)
	Close(context.Context, domain.Order) error
}

package domain

import "time"

type OrderStatus string

const (
	StatusPending  OrderStatus = "pending"
	StatusPaid     OrderStatus = "paid"
	StatusClosed   OrderStatus = "closed"
	StatusFailed   OrderStatus = "failed"
	StatusRefunded OrderStatus = "refunded"
)

type Order struct {
	ID                    string
	CheckoutToken         string
	MerchantOrderNo       string
	Channel               string
	Name                  string
	AmountCents           int64
	Status                OrderStatus
	NotifyURL             string
	ReturnURL             string
	ProviderTransactionID string
	ProviderPayURL        string
	ProviderPayload       []byte
	CreatedAt             time.Time
	UpdatedAt             time.Time
	PaidAt                *time.Time
	ExpiresAt             time.Time
}

type CreateOrder struct {
	MerchantOrderNo string
	Channel         string
	Name            string
	AmountCents     int64
	NotifyURL       string
	ReturnURL       string
	ExpiresAt       time.Time
}

type PaymentConfirmation struct {
	OrderID               string
	ProviderTransactionID string
	ProviderPayload       []byte
	PaidAt                time.Time
}

type Delivery struct {
	ID          int64
	OrderID     string
	NotifyURL   string
	Attempts    int
	NextAttempt time.Time
}

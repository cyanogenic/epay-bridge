package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"epay-bridge/internal/config"
	"epay-bridge/internal/domain"
	"epay-bridge/internal/epay"

	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
)

type Alipay struct {
	client          *alipay.Client
	appID           string
	publicKey       string
	publicCert      []byte
	certificateMode bool
}

func NewAlipay(cfg config.Alipay, publicBaseURL string) (*Alipay, error) {
	privateKey, err := os.ReadFile(cfg.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read Alipay private key: %w", err)
	}
	client, err := alipay.NewClient(cfg.AppID, string(privateKey), cfg.Production)
	if err != nil {
		return nil, fmt.Errorf("initialize Alipay client: %w", err)
	}
	client.SetNotifyUrl(publicBaseURL + "/callbacks/alipay")
	client.SetReturnUrl(publicBaseURL + "/returns/alipay")

	provider := &Alipay{client: client, appID: cfg.AppID}
	if cfg.AppPublicCertFile != "" && cfg.RootCertFile != "" && cfg.PublicCertFile != "" {
		appCert, err := os.ReadFile(cfg.AppPublicCertFile)
		if err != nil {
			return nil, fmt.Errorf("read Alipay app public certificate: %w", err)
		}
		rootCert, err := os.ReadFile(cfg.RootCertFile)
		if err != nil {
			return nil, fmt.Errorf("read Alipay root certificate: %w", err)
		}
		publicCert, err := os.ReadFile(cfg.PublicCertFile)
		if err != nil {
			return nil, fmt.Errorf("read Alipay public certificate: %w", err)
		}
		if err := client.SetCertSnByContent(appCert, rootCert, publicCert); err != nil {
			return nil, fmt.Errorf("configure Alipay certificates: %w", err)
		}
		client.AutoVerifySign(publicCert)
		provider.publicCert = publicCert
		provider.certificateMode = true
	} else {
		publicKey, err := os.ReadFile(cfg.PublicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read Alipay public key: %w", err)
		}
		provider.publicKey = string(publicKey)
	}
	return provider, nil
}

func (a *Alipay) Name() string { return "alipay" }

func (a *Alipay) Create(ctx context.Context, order domain.Order) (*Checkout, error) {
	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", order.MerchantOrderNo).
		Set("total_amount", epay.FormatCents(order.AmountCents)).
		Set("subject", order.Name).
		Set("timeout_express", durationMinutes(time.Until(order.ExpiresAt)))
	payURL, err := a.client.TradePagePay(ctx, bm)
	if err != nil {
		return nil, err
	}
	return &Checkout{Kind: CheckoutRedirect, URL: payURL}, nil
}

func (a *Alipay) ParseNotify(req *http.Request) (*Event, error) {
	bm, err := alipay.ParseNotifyToBodyMap(req)
	if err != nil {
		return nil, err
	}
	verified := false
	if a.certificateMode {
		verified, err = alipay.VerifySignWithCert(a.publicCert, bm)
	} else {
		verified, err = alipay.VerifySign(a.publicKey, bm)
	}
	if err != nil || !verified {
		return nil, errors.New("invalid Alipay notification signature")
	}
	if bm.GetString("app_id") != a.appID {
		return nil, errors.New("Alipay notification app_id mismatch")
	}
	amount, err := epay.ParseCents(bm.GetString("total_amount"))
	if err != nil {
		return nil, errors.New("invalid Alipay notification amount")
	}
	state := PaymentPending
	switch bm.GetString("trade_status") {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		state = PaymentPaid
	case "TRADE_CLOSED":
		state = PaymentClosed
	}
	payload, _ := json.Marshal(map[string]string{
		"notify_id": bm.GetString("notify_id"), "trade_status": bm.GetString("trade_status"),
	})
	return &Event{
		MerchantOrderNo: bm.GetString("out_trade_no"), ProviderTransactionID: bm.GetString("trade_no"),
		AmountCents: amount, State: state, PaidAt: parseAlipayTime(bm.GetString("gmt_payment")), Payload: payload,
	}, nil
}

func (a *Alipay) Query(ctx context.Context, order domain.Order) (*Event, error) {
	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", order.MerchantOrderNo)
	rsp, err := a.client.TradeQuery(ctx, bm)
	if err != nil {
		return nil, err
	}
	if rsp == nil || rsp.Response == nil {
		return nil, errors.New("empty Alipay query response")
	}
	if !a.certificateMode {
		verified, verifyErr := alipay.VerifySyncSign(a.publicKey, rsp.SignData, rsp.Sign)
		if verifyErr != nil || !verified {
			return nil, errors.New("invalid Alipay query response signature")
		}
	}
	amount, err := epay.ParseCents(rsp.Response.TotalAmount)
	if err != nil {
		return nil, err
	}
	state := PaymentPending
	switch rsp.Response.TradeStatus {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		state = PaymentPaid
	case "TRADE_CLOSED":
		state = PaymentClosed
	}
	payload, _ := json.Marshal(map[string]string{"source": "query", "trade_status": rsp.Response.TradeStatus})
	return &Event{
		MerchantOrderNo: rsp.Response.OutTradeNo, ProviderTransactionID: rsp.Response.TradeNo,
		AmountCents: amount, State: state, PaidAt: parseAlipayTime(rsp.Response.SendPayDate), Payload: payload,
	}, nil
}

func (a *Alipay) Close(ctx context.Context, order domain.Order) error {
	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", order.MerchantOrderNo)
	rsp, err := a.client.TradeClose(ctx, bm)
	if err != nil {
		return err
	}
	if rsp == nil || rsp.Response == nil {
		return errors.New("empty Alipay close response")
	}
	if !a.certificateMode {
		verified, verifyErr := alipay.VerifySyncSign(a.publicKey, rsp.SignData, rsp.Sign)
		if verifyErr != nil || !verified {
			return errors.New("invalid Alipay close response signature")
		}
	}
	return nil
}

func durationMinutes(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("%dm", minutes)
}

func parseAlipayTime(value string) time.Time {
	if value == "" {
		return time.Now().UTC()
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	if location == nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(value), location)
	if err != nil {
		return time.Now().UTC()
	}
	return parsed.UTC()
}

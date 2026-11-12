package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
	"unicode/utf8"

	"epay-bridge/internal/config"
	"epay-bridge/internal/domain"

	"github.com/go-pay/gopay"
	wechat "github.com/go-pay/gopay/wechat/v3"
)

type WeChat struct {
	client     *wechat.ClientV3
	appID      string
	merchantID string
	apiV3Key   string
	notifyURL  string
}

func NewWeChat(cfg config.WeChat, publicBaseURL string) (*WeChat, error) {
	privateKey, err := os.ReadFile(cfg.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read WeChat merchant private key: %w", err)
	}
	publicKey, err := os.ReadFile(cfg.PublicKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read WeChat payment public key: %w", err)
	}
	client, err := wechat.NewClientV3(cfg.MerchantID, cfg.SerialNumber, cfg.APIv3Key, string(privateKey))
	if err != nil {
		return nil, fmt.Errorf("initialize WeChat Pay client: %w", err)
	}
	if err := client.AutoVerifySignByPublicKey(publicKey, cfg.PublicKeyID); err != nil {
		return nil, fmt.Errorf("configure WeChat payment public key: %w", err)
	}
	return &WeChat{
		client: client, appID: cfg.AppID, merchantID: cfg.MerchantID,
		apiV3Key: cfg.APIv3Key, notifyURL: publicBaseURL + "/callbacks/wechat",
	}, nil
}

func (w *WeChat) Name() string { return "wxpay" }

func (w *WeChat) Create(ctx context.Context, order domain.Order) (*Checkout, error) {
	bm := make(gopay.BodyMap)
	bm.Set("appid", w.appID).
		Set("mchid", w.merchantID).
		Set("description", truncateUTF8(order.Name, 127)).
		Set("out_trade_no", order.MerchantOrderNo).
		Set("time_expire", order.ExpiresAt.Format(time.RFC3339)).
		Set("notify_url", w.notifyURL).
		SetBodyMap("amount", func(amount gopay.BodyMap) {
			amount.Set("total", order.AmountCents).Set("currency", "CNY")
		})
	rsp, err := w.client.V3TransactionNative(ctx, bm)
	if err != nil {
		return nil, err
	}
	if rsp == nil || rsp.Code != wechat.Success || rsp.Response == nil || rsp.Response.CodeUrl == "" {
		if rsp == nil {
			return nil, errors.New("empty WeChat Native response")
		}
		return nil, fmt.Errorf("WeChat Native order failed: code=%d provider_code=%s message=%s", rsp.Code, rsp.ErrResponse.Code, rsp.ErrResponse.Message)
	}
	return &Checkout{Kind: CheckoutQRCode, URL: rsp.Response.CodeUrl}, nil
}

func (w *WeChat) ParseNotify(req *http.Request) (*Event, error) {
	notify, err := wechat.V3ParseNotify(req)
	if err != nil {
		return nil, err
	}
	if err := notify.VerifySignByPKMap(w.client.WxPublicKeyMap()); err != nil {
		return nil, fmt.Errorf("invalid WeChat notification signature: %w", err)
	}
	result, err := notify.DecryptPayCipherText(w.apiV3Key)
	if err != nil {
		return nil, err
	}
	if result.Appid != w.appID || result.Mchid != w.merchantID {
		return nil, errors.New("WeChat notification merchant identity mismatch")
	}
	if result.Amount == nil {
		return nil, errors.New("WeChat notification has no amount")
	}
	state := wechatState(result.TradeState)
	payload, _ := json.Marshal(map[string]string{"event_id": notify.Id, "event_type": notify.EventType, "trade_state": result.TradeState})
	return &Event{
		MerchantOrderNo: result.OutTradeNo, ProviderTransactionID: result.TransactionId,
		AmountCents: int64(result.Amount.Total), State: state, PaidAt: parseRFC3339(result.SuccessTime), Payload: payload,
	}, nil
}

func (w *WeChat) Query(ctx context.Context, order domain.Order) (*Event, error) {
	rsp, err := w.client.V3TransactionQueryOrder(ctx, wechat.OutTradeNo, order.MerchantOrderNo)
	if err != nil {
		return nil, err
	}
	if rsp == nil || rsp.Code != wechat.Success || rsp.Response == nil {
		if rsp == nil {
			return nil, errors.New("empty WeChat query response")
		}
		return nil, fmt.Errorf("WeChat query failed: code=%d provider_code=%s message=%s", rsp.Code, rsp.ErrResponse.Code, rsp.ErrResponse.Message)
	}
	if rsp.Response.Amount == nil {
		return nil, errors.New("WeChat query response has no amount")
	}
	payload, _ := json.Marshal(map[string]string{"source": "query", "trade_state": rsp.Response.TradeState})
	return &Event{
		MerchantOrderNo: rsp.Response.OutTradeNo, ProviderTransactionID: rsp.Response.TransactionId,
		AmountCents: int64(rsp.Response.Amount.Total), State: wechatState(rsp.Response.TradeState),
		PaidAt: parseRFC3339(rsp.Response.SuccessTime), Payload: payload,
	}, nil
}

func (w *WeChat) Close(ctx context.Context, order domain.Order) error {
	rsp, err := w.client.V3TransactionCloseOrder(ctx, order.MerchantOrderNo)
	if err != nil {
		return err
	}
	if rsp == nil || rsp.Code != wechat.Success {
		if rsp == nil {
			return errors.New("empty WeChat close response")
		}
		return fmt.Errorf("WeChat close failed: code=%d provider_code=%s message=%s", rsp.Code, rsp.ErrResponse.Code, rsp.ErrResponse.Message)
	}
	return nil
}

func wechatState(value string) PaymentState {
	switch value {
	case wechat.TradeStateSuccess:
		return PaymentPaid
	case wechat.TradeStateClosed, wechat.TradeStateRevoked, wechat.TradeStatePayError:
		return PaymentClosed
	default:
		return PaymentPending
	}
}

func parseRFC3339(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Now().UTC()
	}
	return parsed.UTC()
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

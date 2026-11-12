package epay

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var orderNumberPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var moneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,9})(\.[0-9]{1,2})?$`)

type PurchaseRequest struct {
	PID             string
	Type            string
	MerchantOrderNo string
	NotifyURL       string
	ReturnURL       string
	Name            string
	Money           string
	AmountCents     int64
	Device          string
}

func ParsePurchase(params url.Values, expectedPID, merchantKey string, maxAmountCents int64, allowedOrigins map[string]struct{}) (*PurchaseRequest, error) {
	flat := make(map[string]string, len(params))
	for key, values := range params {
		if len(values) != 1 {
			return nil, fmt.Errorf("parameter %q must appear exactly once", key)
		}
		flat[key] = values[0]
	}
	if flat["pid"] != expectedPID {
		return nil, errors.New("invalid merchant")
	}
	if !strings.EqualFold(flat["sign_type"], "MD5") {
		return nil, errors.New("unsupported signature type")
	}
	ok, err := Verify(flat, merchantKey)
	if err != nil || !ok {
		return nil, errors.New("invalid signature")
	}
	if flat["type"] != "alipay" && flat["type"] != "wxpay" {
		return nil, errors.New("unsupported payment type")
	}
	if !orderNumberPattern.MatchString(flat["out_trade_no"]) {
		return nil, errors.New("invalid merchant order number")
	}
	if flat["name"] == "" || len([]rune(flat["name"])) > 128 {
		return nil, errors.New("invalid order name")
	}
	amount, err := ParseCents(flat["money"])
	if err != nil || amount < 1 || amount > maxAmountCents {
		return nil, errors.New("invalid amount")
	}
	if err := validateCallbackURL(flat["notify_url"], allowedOrigins); err != nil {
		return nil, fmt.Errorf("invalid notify_url: %w", err)
	}
	if err := validateCallbackURL(flat["return_url"], allowedOrigins); err != nil {
		return nil, fmt.Errorf("invalid return_url: %w", err)
	}
	return &PurchaseRequest{
		PID:             flat["pid"],
		Type:            flat["type"],
		MerchantOrderNo: flat["out_trade_no"],
		NotifyURL:       flat["notify_url"],
		ReturnURL:       flat["return_url"],
		Name:            flat["name"],
		Money:           FormatCents(amount),
		AmountCents:     amount,
		Device:          flat["device"],
	}, nil
}

func ParseCents(value string) (int64, error) {
	if !moneyPattern.MatchString(value) {
		return 0, errors.New("invalid money format")
	}
	parts := strings.SplitN(value, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	fraction := int64(0)
	if len(parts) == 2 {
		fractionText := parts[1]
		if len(fractionText) == 1 {
			fractionText += "0"
		}
		fraction, err = strconv.ParseInt(fractionText, 10, 64)
		if err != nil {
			return 0, err
		}
	}
	return whole*100 + fraction, nil
}

func FormatCents(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func validateCallbackURL(raw string, allowedOrigins map[string]struct{}) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Fragment != "" {
		return errors.New("must be an absolute HTTPS URL")
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	if _, ok := allowedOrigins[origin]; !ok {
		return errors.New("origin is not allowlisted")
	}
	return nil
}

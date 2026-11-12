# EPay compatibility contract

The gateway implements the subset used by New API's `go-epay` client.

## Purchase

`GET` or form-encoded `POST /submit.php` accepts:

- `pid`
- `type`: `alipay` or `wxpay`
- `out_trade_no`
- `notify_url`
- `return_url`
- `name`
- `money`
- `device`
- `sign_type=MD5`
- `sign`

The signature removes `sign`, `sign_type`, and empty values, sorts remaining
keys, joins the original values as `key=value&key=value`, appends the EPay key,
and computes lowercase MD5. This weak algorithm exists only at the New API
compatibility boundary. Provider traffic uses Alipay RSA2 and WeChat Pay API v3
RSA/AES-GCM.

Callback and return origins must match `NEW_API_ALLOWED_ORIGINS`. Amounts are
parsed as strict decimal CNY and stored as integer cents.

## Successful callback

After a provider callback is authenticated and its merchant identity, order
number, currency, and amount match the stored order, the gateway sends a
form-encoded POST to the order's `notify_url`:

- `pid`
- `trade_no`: gateway order ID
- `out_trade_no`: original New API order ID
- `type`
- `name`
- `money`
- `trade_status=TRADE_SUCCESS`
- `sign_type=MD5`
- `sign`

Only HTTP 2xx with a response body equal to `success` acknowledges delivery.
Failures are retried durably with a capped backoff. Payment state and callback
creation are committed in the same PostgreSQL transaction.


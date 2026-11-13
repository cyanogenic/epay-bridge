# Configuration

## 1. Payment credentials

Never commit provider keys or certificates. Create this local layout; the
entire `secrets/` directory is ignored by Git and the Docker build context.
Each provider is optional, but at least one must be enabled. Setting
`ALIPAY_APP_ID` enables Alipay, while setting `WECHAT_MERCHANT_ID` enables
WeChat Pay. Once enabled, all credentials for that provider are required.

```text
secrets/
├── alipay/
│   ├── app_private_key.pem
│   ├── appCertPublicKey_APPID.crt
│   ├── alipayRootCert.crt
│   └── alipayCertPublicKey_RSA2.crt
└── wechat/
    ├── apiclient_key.pem
    └── wechatpay_public_key.pem
```

Alipay certificate mode is the default Compose configuration. The application
also supports Alipay public-key mode by leaving all three certificate variables
empty and setting `ALIPAY_PUBLIC_KEY_FILE`.

`SECRETS_HOST_DIR` can point to a different host directory. The individual
`*_FILE` settings are paths inside the container and default to files below
`/run/secrets`. Set `ALIPAY_APP_PUBLIC_CERT_FILE` to the exact filename supplied
by Alipay, for example
`/run/secrets/alipay/appCertPublicKey_2021000000000000.crt`; renaming or copying
the downloaded certificate is not required.

For WeChat Pay, `WECHAT_PUBLIC_KEY_ID` must match the public key file. Preserve
the `PUB_KEY_ID_` prefix when present. The merchant API certificate serial
number belongs in `WECHAT_SERIAL_NUMBER`; these are different identifiers.

## 2. Environment

Copy `.env.example` to `.env` and fill it locally. Generate unrelated secrets,
for example with `openssl rand -hex 32`. `EPAY_KEY` must be at least 32
characters and `WECHAT_API_V3_KEY` must be exactly 32 characters.

`NEW_API_ALLOWED_ORIGINS` is a strict SSRF/open-redirect boundary. It must be
the exact HTTPS origin of the New API instance, without a path. Multiple
origins are supported only for deployments where the same instance is
legitimately reachable through multiple hostnames.

## 3. Reverse proxy

Terminate HTTPS at the existing reverse proxy and forward the payment hostname
to `127.0.0.1:8080`. Preserve the original request method, body, and headers.
Enabled provider callbacks are:

- `https://pay.example.com/callbacks/alipay`
- `https://pay.example.com/callbacks/wechat`

Do not cache, authenticate, rewrite, or browser-challenge either callback.
Apply ordinary request-size and rate limits, but allow official provider retry
traffic.

## 4. New API settings

Configure New API's EPay fields:

```text
API address: https://pay.example.com
PID:         value of EPAY_PID
KEY:         value of EPAY_KEY
```

Configure only the payment methods whose providers are enabled. For both
providers, use:

```json
[
  {"name":"支付宝","type":"alipay","color":"#1677FF","min_topup":"1"},
  {"name":"微信支付","type":"wxpay","color":"#07C160","min_topup":"1"}
]
```

Also complete New API's payment-compliance confirmation. The gateway accepts
both the balance top-up callback and subscription callback because it stores
the signed `notify_url` and `return_url` supplied with each order.

## 5. Start and verify

Production uses the public `ghcr.io/cyanogenic/epay-bridge:latest` image:

```sh
docker compose config
docker compose pull
docker compose up -d --wait
docker compose ps
curl -fsS https://pay.example.com/readyz
```

For local source builds, add the development override:

```sh
docker compose -f compose.yaml -f compose.build.yaml up -d --build --wait
```

Use provider production accounts and a CNY 0.01 test order first. For each
enabled provider, verify the applicable steps before enabling normal amounts:

1. Alipay returns to New API and its asynchronous callback is accepted.
2. The WeChat QR code renders and its API v3 callback is accepted.
3. New API credits a top-up exactly once when callbacks are repeated.
4. A subscription order activates exactly once.
5. The gateway order page shows the official provider transaction ID.


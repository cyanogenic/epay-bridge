package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"epay-bridge/internal/config"
	"epay-bridge/internal/domain"
	"epay-bridge/internal/epay"
	"epay-bridge/internal/provider"
	"epay-bridge/internal/service"
	"epay-bridge/internal/store"

	qrcode "github.com/skip2/go-qrcode"
)

type Server struct {
	cfg      config.Config
	store    *store.Store
	payments *service.Payments
	mux      *http.ServeMux
	checkout *template.Template
	admin    *template.Template
}

func NewServer(cfg config.Config, store *store.Store, payments *service.Payments) (*Server, error) {
	checkout, err := template.New("checkout").Parse(checkoutHTML)
	if err != nil {
		return nil, err
	}
	admin, err := template.New("admin").Funcs(template.FuncMap{"money": epay.FormatCents}).Parse(adminHTML)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: store, payments: payments, mux: http.NewServeMux(), checkout: checkout, admin: admin}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return requestLog(securityHeaders(s.mux)) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.HandleFunc("GET /submit.php", s.submit)
	s.mux.HandleFunc("POST /submit.php", s.submit)
	s.mux.HandleFunc("POST /callbacks/alipay", s.alipayNotify)
	s.mux.HandleFunc("POST /callbacks/wechat", s.wechatNotify)
	s.mux.HandleFunc("GET /returns/alipay", s.alipayReturn)
	s.mux.HandleFunc("POST /returns/alipay", s.alipayReturn)
	s.mux.HandleFunc("GET /checkout/{token}", s.checkoutPage)
	s.mux.HandleFunc("GET /checkout/{token}/qr.png", s.checkoutQR)
	s.mux.HandleFunc("GET /api/checkout/{token}/status", s.checkoutStatus)
	s.mux.HandleFunc("GET /complete/{token}", s.complete)
	s.mux.HandleFunc("GET /admin/orders", s.adminOrders)
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if r.Method == http.MethodPost {
		contentType := strings.ToLower(r.Header.Get("Content-Type"))
		if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") && !strings.HasPrefix(contentType, "multipart/form-data") {
			http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
			return
		}
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid payment request", http.StatusBadRequest)
		return
	}
	req, err := epay.ParsePurchase(r.Form, s.cfg.EPayPID, s.cfg.EPayKey, s.cfg.MaxAmountCents, s.cfg.AllowedOrigins)
	if err != nil {
		slog.Warn("rejected EPay request", "remote", r.RemoteAddr, "error", err)
		http.Error(w, "invalid payment request", http.StatusBadRequest)
		return
	}
	order, kind, err := s.payments.Create(r.Context(), req)
	if err != nil {
		slog.Error("create payment", "merchant_order_no", req.MerchantOrderNo, "channel", req.Type, "error", err)
		http.Error(w, "unable to create payment", http.StatusBadGateway)
		return
	}
	if order.Status == domain.StatusPaid {
		s.redirectComplete(w, r, *order)
		return
	}
	if kind == provider.CheckoutRedirect {
		http.Redirect(w, r, order.ProviderPayURL, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/checkout/"+url.PathEscape(order.CheckoutToken), http.StatusSeeOther)
}

func (s *Server) alipayNotify(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if _, err := s.payments.ProcessNotify(r.Context(), "alipay", r); err != nil {
		slog.Warn("Alipay callback rejected", "remote", r.RemoteAddr, "error", err)
		http.Error(w, "failure", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("success"))
}

func (s *Server) wechatNotify(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if _, err := s.payments.ProcessNotify(r.Context(), "wxpay", r); err != nil {
		slog.Warn("WeChat callback rejected", "remote", r.RemoteAddr, "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "FAIL", "message": "invalid notification"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) alipayReturn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid return", http.StatusBadRequest)
		return
	}
	merchantOrderNo := r.Form.Get("out_trade_no")
	order, err := s.store.GetOrderByMerchantNo(r.Context(), merchantOrderNo)
	if err != nil || order.Channel != "alipay" {
		http.Error(w, "order not found", http.StatusNotFound)
		return
	}
	if order.Status == domain.StatusPending {
		if err := s.payments.ReconcileOrder(r.Context(), *order); err != nil {
			slog.Warn("reconcile Alipay browser return", "order_id", order.ID, "error", err)
		}
		order, _ = s.store.GetOrderByID(r.Context(), order.ID)
	}
	if order.Status == domain.StatusPaid {
		s.redirectComplete(w, r, *order)
		return
	}
	http.Redirect(w, r, "/checkout/"+url.PathEscape(order.CheckoutToken), http.StatusSeeOther)
}

func (s *Server) checkoutPage(w http.ResponseWriter, r *http.Request) {
	order, err := s.store.GetOrderByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if order.Channel == "alipay" && order.Status == domain.StatusPending {
		http.Redirect(w, r, order.ProviderPayURL, http.StatusSeeOther)
		return
	}
	data := struct {
		Token, Name, Amount, Status string
		ExpiresAt                   time.Time
	}{order.CheckoutToken, order.Name, epay.FormatCents(order.AmountCents), string(order.Status), order.ExpiresAt}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.checkout.Execute(w, data); err != nil {
		slog.Error("render checkout", "error", err)
	}
}

func (s *Server) checkoutQR(w http.ResponseWriter, r *http.Request) {
	order, err := s.store.GetOrderByToken(r.Context(), r.PathValue("token"))
	if err != nil || order.Channel != "wxpay" || order.ProviderPayURL == "" {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(order.ProviderPayURL, qrcode.Medium, 320)
	if err != nil {
		http.Error(w, "unable to render QR code", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) checkoutStatus(w http.ResponseWriter, r *http.Request) {
	order, err := s.store.GetOrderByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": string(order.Status), "complete_url": "/complete/" + order.CheckoutToken,
	})
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request) {
	order, err := s.store.GetOrderByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if order.Status == domain.StatusPending {
		http.Redirect(w, r, "/checkout/"+order.CheckoutToken, http.StatusSeeOther)
		return
	}
	s.redirectComplete(w, r, *order)
}

func (s *Server) redirectComplete(w http.ResponseWriter, r *http.Request, order domain.Order) {
	target, err := s.payments.ReturnURL(order)
	if err != nil {
		http.Error(w, "unable to return to merchant", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	s.health(w, r)
}

func (s *Server) adminOrders(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminUsername == "" || s.cfg.AdminPassword == "" {
		http.NotFound(w, r)
		return
	}
	username, password, ok := r.BasicAuth()
	if !ok || !constantTimeEqual(username, s.cfg.AdminUsername) || !constantTimeEqual(password, s.cfg.AdminPassword) {
		w.Header().Set("WWW-Authenticate", `Basic realm="EPay Bridge"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	orders, err := s.store.RecentOrders(r.Context(), 200)
	if err != nil {
		http.Error(w, "unable to load orders", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.admin.Execute(w, orders); err != nil {
		slog.Error("render admin orders", "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("http request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "duration_ms", time.Since(start).Milliseconds())
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self' https://*.alipay.com; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

const checkoutHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>微信支付</title><style>
body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;margin:0;color:#182230}.card{max-width:460px;margin:7vh auto;background:#fff;border-radius:18px;padding:34px;box-shadow:0 18px 55px #0b1f3318;text-align:center}.qr{width:320px;height:320px;max-width:100%;border-radius:8px}.amount{font-size:34px;font-weight:700;margin:12px}.muted{color:#667085}.status{margin-top:20px;padding:12px;border-radius:10px;background:#f2f4f7}@media(max-width:540px){.card{margin:0;min-height:100vh;border-radius:0;padding:24px}}
</style></head><body><main class="card"><h1>微信扫码支付</h1><div class="amount">¥ {{.Amount}}</div><p>{{.Name}}</p>
{{if eq .Status "pending"}}<img class="qr" src="/checkout/{{.Token}}/qr.png" alt="微信支付二维码"><p class="muted">请使用微信扫一扫完成支付</p><div class="status" id="status">等待支付…</div>
<script>const token={{printf "%q" .Token}};async function poll(){try{const r=await fetch('/api/checkout/'+token+'/status',{cache:'no-store'});if(r.ok){const j=await r.json();if(j.status==='paid'||j.status==='closed'||j.status==='failed'){location.href=j.complete_url;return}}}catch(e){}setTimeout(poll,2000)}setTimeout(poll,1200)</script>
{{else}}<div class="status">订单状态：{{.Status}}</div><p><a href="/complete/{{.Token}}">返回商户</a></p>{{end}}</main></body></html>`

const adminHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>EPay Bridge Orders</title><style>
body{font-family:system-ui;margin:30px;color:#182230}table{border-collapse:collapse;width:100%;font-size:14px}th,td{padding:10px;border-bottom:1px solid #e4e7ec;text-align:left}th{background:#f9fafb}.paid{color:#067647}.pending{color:#b54708}</style></head><body><h1>最近订单</h1><table><thead><tr><th>创建时间</th><th>New API 订单</th><th>渠道</th><th>金额</th><th>状态</th><th>官方交易号</th></tr></thead><tbody>
{{range .}}<tr><td>{{.CreatedAt.Format "2006-01-02 15:04:05"}}</td><td>{{.MerchantOrderNo}}</td><td>{{.Channel}}</td><td>¥ {{money .AmountCents}}</td><td class="{{.Status}}">{{.Status}}</td><td>{{.ProviderTransactionID}}</td></tr>{{end}}
</tbody></table></body></html>`

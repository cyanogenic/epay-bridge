package epay

import (
	"crypto/md5" // #nosec G501 -- required only at the legacy EPay compatibility boundary.
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

var ErrAmbiguousParameter = errors.New("epay parameter contains an ambiguous delimiter")

// Sign implements the legacy EPay MD5 contract: remove sign/sign_type/empty
// values, sort by key, join as k=v&k=v, append the merchant key, then MD5.
// MD5 is not used for provider authentication; Alipay and WeChat use their
// official asymmetric-signature protocols.
func Sign(params map[string]string, merchantKey string) (string, error) {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		if k == "" || strings.ContainsAny(k, "&=") || strings.Contains(v, "&") {
			return "", ErrAmbiguousParameter
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	b.WriteString(merchantKey)
	sum := md5.Sum([]byte(b.String())) // #nosec G401 -- protocol compatibility, not a security primitive.
	return hex.EncodeToString(sum[:]), nil
}

func Verify(params map[string]string, merchantKey string) (bool, error) {
	provided, err := hex.DecodeString(strings.ToLower(params["sign"]))
	if err != nil || len(provided) != md5.Size {
		return false, nil
	}
	expectedHex, err := Sign(params, merchantKey)
	if err != nil {
		return false, err
	}
	expected, _ := hex.DecodeString(expectedHex)
	return subtle.ConstantTimeCompare(provided, expected) == 1, nil
}

func SignedCopy(params map[string]string, merchantKey string) (map[string]string, error) {
	out := make(map[string]string, len(params)+2)
	for k, v := range params {
		out[k] = v
	}
	out["sign_type"] = "MD5"
	sign, err := Sign(out, merchantKey)
	if err != nil {
		return nil, err
	}
	out["sign"] = sign
	return out, nil
}

package providers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// SignV4 signs req in place with AWS Signature Version 4 (header-based).
// Path segments are double-encoded as required for non-S3 services.
func SignV4(req *http.Request, body []byte, cr AWSCreds, region, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	date := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	if cr.Token != "" {
		req.Header.Set("X-Amz-Security-Token", cr.Token)
	}
	host := req.URL.Host
	if req.Host != "" {
		host = req.Host
	}

	// Canonical headers: host plus every x-amz-* and content-type header.
	hdrs := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "content-type" {
			hdrs[lk] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
		}
	}
	names := make([]string, 0, len(hdrs))
	for k := range hdrs {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n)
		ch.WriteByte(':')
		ch.WriteString(hdrs[n])
		ch.WriteByte('\n')
	}
	signed := strings.Join(names, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonPath := awsURIEncode(path, false)

	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cq []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			cq = append(cq, awsURIEncode(k, true)+"="+awsURIEncode(v, true))
		}
	}

	ph := sha256.Sum256(body)
	canonical := strings.Join([]string{req.Method, canonPath, strings.Join(cq, "&"), ch.String(), signed, hex.EncodeToString(ph[:])}, "\n")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	ch2 := sha256.Sum256([]byte(canonical))
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(ch2[:])

	k := hmacSHA256([]byte("AWS4"+cr.Secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cr.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// awsURIEncode implements the SigV4 UriEncode function. When encodeSlash is
// false, '/' is preserved (used for paths).
func awsURIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteString("%")
			b.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

// bedrockModelPath builds an escaped path segment for a model id / ARN.
func bedrockModelPath(model string) string {
	return strings.ReplaceAll(url.PathEscape(model), ":", "%3A")
}

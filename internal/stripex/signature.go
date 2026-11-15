// Package stripex contains small, dependency-free helpers for talking to Stripe:
// webhook signature verification and a minimal REST client for meter events.
package stripex

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// DefaultTolerance is the maximum age of a webhook signature that we accept.
// Stripe recommends 5 minutes to protect against replay attacks.
const DefaultTolerance = 5 * time.Minute

var (
	ErrMissingHeader    = errors.New("stripex: missing Stripe-Signature header")
	ErrInvalidHeader    = errors.New("stripex: malformed Stripe-Signature header")
	ErrNoValidSignature = errors.New("stripex: no matching v1 signature")
	ErrTooOld           = errors.New("stripex: signature timestamp outside tolerance")
)

// VerifySignature checks a Stripe-Signature header against the raw request body.
//
// Header format: "t=1492774577,v1=5257a869e7ec...,v1=..."
// The signed payload is "<timestamp>.<raw body>" HMAC-SHA256'd with the endpoint secret.
func VerifySignature(payload []byte, header, secret string, tolerance time.Duration, now time.Time) error {
	if header == "" {
		return ErrMissingHeader
	}

	var ts int64
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ErrInvalidHeader
			}
			ts = n
		case "v1":
			b, err := hex.DecodeString(v)
			if err != nil {
				continue
			}
			sigs = append(sigs, b)
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return ErrInvalidHeader
	}

	if tolerance > 0 {
		age := now.Sub(time.Unix(ts, 0))
		if age > tolerance || age < -tolerance {
			return ErrTooOld
		}
	}

	expected := ComputeSignature(ts, payload, secret)
	for _, s := range sigs {
		if hmac.Equal(s, expected) {
			return nil
		}
	}
	return ErrNoValidSignature
}

// ComputeSignature returns the raw HMAC for a timestamp + payload. Exposed for tests
// and for generating signed requests locally.
func ComputeSignature(ts int64, payload []byte, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return mac.Sum(nil)
}

// SignHeader builds a valid Stripe-Signature header (useful in tests and local tools).
func SignHeader(ts int64, payload []byte, secret string) string {
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(ComputeSignature(ts, payload, secret))
}

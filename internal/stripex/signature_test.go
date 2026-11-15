package stripex

import (
	"errors"
	"testing"
	"time"
)

func TestVerifySignature(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_1","type":"invoice.paid"}`)
	now := time.Unix(1_700_000_000, 0)
	valid := SignHeader(now.Unix(), payload, secret)

	tests := []struct {
		name    string
		header  string
		payload []byte
		now     time.Time
		want    error
	}{
		{"valid", valid, payload, now, nil},
		{"missing header", "", payload, now, ErrMissingHeader},
		{"garbage header", "nonsense", payload, now, ErrInvalidHeader},
		{"tampered body", valid, []byte(`{"id":"evt_2"}`), now, ErrNoValidSignature},
		{"wrong secret", SignHeader(now.Unix(), payload, "whsec_other"), payload, now, ErrNoValidSignature},
		{"replayed too late", valid, payload, now.Add(10 * time.Minute), ErrTooOld},
		{"multiple v1 one valid", valid + ",v1=deadbeef", payload, now, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifySignature(tc.payload, tc.header, secret, DefaultTolerance, tc.now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

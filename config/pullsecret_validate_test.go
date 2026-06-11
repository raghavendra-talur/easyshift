package config

import (
	"strings"
	"testing"
)

func TestValidatePullSecretBytes(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string // empty = expect success
	}{
		{name: "valid", data: `{"auths":{"quay.io":{"auth":"Zm9v"}}}`},
		{name: "not json", data: `not-json`, wantErr: "not valid JSON"},
		{name: "missing auths", data: `{"foo":1}`, wantErr: "missing required 'auths' key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePullSecretBytes([]byte(tt.data))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidatePullSecretBytes() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidatePullSecretBytes() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

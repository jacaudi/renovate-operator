package kvstore_test

import (
	"testing"

	"renovate-operator/internal/kvstore"
)

func TestValidateMultiDB(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{value: "", wantErr: false},
		{value: "true", wantErr: false},
		{value: "false", wantErr: false},
		{value: "True", wantErr: true},
		{value: "1", wantErr: true},
		{value: "yes", wantErr: true},
		{value: " true", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			err := kvstore.ValidateMultiDB(tt.value)
			if got := err != nil; got != tt.wantErr {
				t.Errorf("ValidateMultiDB(%q) error = %v, want error: %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

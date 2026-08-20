package gateway

import (
	"reflect"
	"testing"

	"github.com/openziti/zrok/v2/sdk/golang/sdk"
)

func TestNewShareRequestIsClosedAndCarriesAccessGrants(t *testing.T) {
	tests := []struct {
		name   string
		grants []string
	}{
		{name: "owner only"},
		{name: "granted accounts", grants: []string{"one@example.com", "two@example.com"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := newShareRequest(tt.grants)
			if request.BackendMode != sdk.ProxyBackendMode {
				t.Fatalf("BackendMode = %v, want %v", request.BackendMode, sdk.ProxyBackendMode)
			}
			if request.ShareMode != sdk.PrivateShareMode {
				t.Fatalf("ShareMode = %v, want %v", request.ShareMode, sdk.PrivateShareMode)
			}
			if request.PermissionMode != sdk.ClosedPermissionMode {
				t.Fatalf("PermissionMode = %v, want %v", request.PermissionMode, sdk.ClosedPermissionMode)
			}
			if !reflect.DeepEqual(request.AccessGrants, tt.grants) {
				t.Fatalf("AccessGrants = %#v, want %#v", request.AccessGrants, tt.grants)
			}
		})
	}
}

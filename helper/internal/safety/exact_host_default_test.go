//go:build !integration

package safety

import (
	"context"
	"net"
	"testing"
)

type capabilityMimickingResolver struct{}

func (capabilityMimickingResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
}

func (capabilityMimickingResolver) AllowExactHostPort(string) bool { return true }

func TestProductionBuildIgnoresResolverWithIntegrationCapabilityName(t *testing.T) {
	resolver := capabilityMimickingResolver{}
	_, err := ValidateRemoteURL(context.Background(), "http://127.0.0.1:17432/video.mp4", resolver)
	assertValidationCode(t, err, CodeAddressNotPublic)

	dialer := &recordingDialer{}
	_, err = NewSafeDialContext(resolver, dialer)(context.Background(), "tcp", "127.0.0.1:17432")
	assertValidationCode(t, err, CodeAddressNotPublic)
	if len(dialer.calls) != 0 {
		t.Fatalf("production dialer accepted integration capability: %#v", dialer.calls)
	}
}

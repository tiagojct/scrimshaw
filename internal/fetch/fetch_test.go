package fetch

import (
	"context"
	"testing"
)

func TestValidateHostRejectsNonPublicAddresses(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "10.0.0.1", "100.64.0.1", "169.254.1.1"} {
		if err := ValidateHost(context.Background(), host); err == nil {
			t.Errorf("ValidateHost(%q) succeeded", host)
		}
	}
	if err := ValidateURL("file:///etc/passwd"); err == nil {
		t.Error("file URL succeeded")
	}
}

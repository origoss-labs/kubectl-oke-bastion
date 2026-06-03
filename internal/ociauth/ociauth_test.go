package ociauth

import (
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
)

// spyBuilders records which builder fired and the profile it received, without
// invoking any real SDK constructor — instance_principal in particular reaches
// the instance metadata endpoint at construction, which must never happen in a
// unit test.
func spyBuilders(fired *string, gotProfile *string) builders {
	profileBuilder := func(name string) func(string) (common.ConfigurationProvider, error) {
		return func(profile string) (common.ConfigurationProvider, error) {
			*fired = name
			*gotProfile = profile
			return nil, nil
		}
	}
	return builders{
		apiKey:        profileBuilder("api_key"),
		securityToken: profileBuilder("security_token"),
		instancePrincipal: func() (common.ConfigurationProvider, error) {
			*fired = "instance_principal"
			return nil, nil
		},
	}
}

func TestProvider_SelectsBuilderPerMethod(t *testing.T) {
	cases := map[Method]string{
		APIKey:            "api_key",
		SecurityToken:     "security_token",
		InstancePrincipal: "instance_principal",
	}
	for method, want := range cases {
		t.Run(string(method), func(t *testing.T) {
			var fired, profile string
			b := spyBuilders(&fired, &profile)
			if _, err := b.provider(Spec{Method: method}); err != nil {
				t.Fatalf("provider(%q) returned error: %v", method, err)
			}
			if fired != want {
				t.Errorf("method %q fired %q builder, want %q", method, fired, want)
			}
		})
	}
}

func TestProvider_APIKeyPropagatesProfile(t *testing.T) {
	var fired, profile string
	b := spyBuilders(&fired, &profile)
	if _, err := b.provider(Spec{Method: APIKey, Profile: "PRODUCTION"}); err != nil {
		t.Fatalf("provider returned error: %v", err)
	}
	if profile != "PRODUCTION" {
		t.Errorf("api_key builder got profile %q, want %q", profile, "PRODUCTION")
	}
}

func TestProvider_UnknownMethodErrors(t *testing.T) {
	var fired, profile string
	b := spyBuilders(&fired, &profile)
	if _, err := b.provider(Spec{Method: "kerberos"}); err == nil {
		t.Fatal("expected an error for an unknown --auth method, got nil")
	}
	if fired != "" {
		t.Errorf("unknown method fired the %q builder; should fire none", fired)
	}
}

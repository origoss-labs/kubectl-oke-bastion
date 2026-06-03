// Package ociauth resolves the operator's --auth/--profile choice into an OCI
// ConfigurationProvider, hiding the three distinct SDK constructors behind one
// Spec → Provider call.
package ociauth

import (
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
)

// Method is an --auth selection.
type Method string

const (
	// APIKey signs with an API key from ~/.oci/config (the default).
	APIKey Method = "api_key"
	// SecurityToken signs with a session token from a browser `oci session authenticate`.
	SecurityToken Method = "security_token"
	// InstancePrincipal signs as the OCI compute instance the plugin runs on.
	InstancePrincipal Method = "instance_principal"
)

// Spec is the operator's auth selection. Profile applies to the config-file
// methods (api_key, security_token) and is ignored by instance_principal.
type Spec struct {
	Method  Method
	Profile string
}

// Provider resolves spec to an OCI ConfigurationProvider.
func Provider(spec Spec) (common.ConfigurationProvider, error) {
	return realBuilders.provider(spec)
}

// builders holds one constructor per method. The real set is realBuilders;
// tests substitute spies so no SDK constructor or live OCI call runs.
type builders struct {
	apiKey            func(profile string) (common.ConfigurationProvider, error)
	securityToken     func(profile string) (common.ConfigurationProvider, error)
	instancePrincipal func() (common.ConfigurationProvider, error)
}

func (b builders) provider(spec Spec) (common.ConfigurationProvider, error) {
	switch spec.Method {
	case APIKey:
		return b.apiKey(spec.Profile)
	case SecurityToken:
		return b.securityToken(spec.Profile)
	case InstancePrincipal:
		return b.instancePrincipal()
	default:
		return nil, fmt.Errorf("unknown --auth %q: want api_key, security_token, or instance_principal", spec.Method)
	}
}

// profileOrDefault maps an empty profile to OCI's DEFAULT profile. The
// session-token provider, unlike the api_key one, has no built-in DEFAULT
// fallback, so an empty profile would otherwise resolve no section at all.
func profileOrDefault(profile string) string {
	if profile == "" {
		return "DEFAULT"
	}
	return profile
}

var realBuilders = builders{
	apiKey: func(profile string) (common.ConfigurationProvider, error) {
		if profile == "" {
			return common.DefaultConfigProvider(), nil
		}
		return common.CustomProfileConfigProvider("", profile), nil
	},
	securityToken: func(profile string) (common.ConfigurationProvider, error) {
		return common.CustomProfileSessionTokenConfigProvider("", profileOrDefault(profile)), nil
	},
	instancePrincipal: func() (common.ConfigurationProvider, error) {
		return auth.InstancePrincipalConfigurationProvider()
	},
}

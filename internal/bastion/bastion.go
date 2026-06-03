// Package bastion talks to an existing OCI Bastion. Slice 2 reads it; later
// slices create, poll, and delete port-forwarding sessions on it.
package bastion

import (
	"context"
	"fmt"

	ocibastion "github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/common"
)

// Handle is a verified reference to a pre-existing Bastion.
type Handle struct {
	ID    string
	Name  string
	State string
}

// NewClient builds an OCI bastion client from a configuration provider. It is
// the single place the SDK constructor is wrapped, shared by Get and by the
// session lifecycle.
func NewClient(cp common.ConfigurationProvider) (ocibastion.BastionClient, error) {
	client, err := ocibastion.NewBastionClientWithConfigurationProvider(cp)
	if err != nil {
		return ocibastion.BastionClient{}, fmt.Errorf("creating bastion client: %w", err)
	}
	return client, nil
}

// Get proves the credentials work by calling GetBastion, returning the
// bastion's name and lifecycle state.
func Get(ctx context.Context, cp common.ConfigurationProvider, id string) (Handle, error) {
	client, err := NewClient(cp)
	if err != nil {
		return Handle{}, err
	}
	resp, err := client.GetBastion(ctx, ocibastion.GetBastionRequest{BastionId: &id})
	if err != nil {
		return Handle{}, fmt.Errorf("getting bastion %s: %w", id, err)
	}
	if resp.Name == nil {
		return Handle{}, fmt.Errorf("getting bastion %s: response carried no name", id)
	}
	return Handle{
		ID:    id,
		Name:  *resp.Name,
		State: string(resp.LifecycleState),
	}, nil
}

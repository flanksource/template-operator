package controllers

import (
	"github.com/flanksource/kommons"
	"github.com/flanksource/template-operator/k8s"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllercliconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	CRDV1Version = "v1"
	CRDV1Group   = "apiextensions.k8s.io"
)

type Client struct {
	ControllerClient client.Client
	KommonsClient    *kommons.Client
	Events           record.EventRecorder
	Log              logr.Logger
	Scheme           *runtime.Scheme
	Cache            *k8s.SchemaCache
	Discovery        discovery.DiscoveryInterface
}

// HasKind detects if the given api group with specified version is supported by the server
func (c *Client) HasKind(groupName, version string) (bool, error) {
	if c.Discovery != nil {
		groups, err := c.Discovery.ServerGroups()
		if err != nil {
			return false, err
		}
		for _, group := range groups.Groups {
			for _, groupVersion := range group.Versions {
				if groupVersion.GroupVersion == groupName+"/"+version {
					return true, nil
				}
			}
		}
		return false, nil
	}
	c.Log.Info("Tried to discover the platform, but no discovery API is available")
	return false, nil
}

func buildKubeConnectionConfig() (*restclient.Config, error) {
	return controllercliconfig.GetConfig()
}

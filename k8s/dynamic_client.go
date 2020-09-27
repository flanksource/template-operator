package k8s

import (
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

type DynamicClient struct {
	Clientset     *kubernetes.Clientset
	DynamicClient dynamic.Interface
	restMapper    meta.RESTMapper

	mtx *sync.Mutex
}

func NewDynamicClient(clientset *kubernetes.Clientset, dynamicClient dynamic.Interface) *DynamicClient {
	client := &DynamicClient{
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		mtx:           &sync.Mutex{},
	}
	return client
}

func (c *DynamicClient) GetClientByKind(kind string, apiVersion string) (dynamic.NamespaceableResourceInterface, error) {
	var group, version string
	parts := strings.Split(apiVersion, "/")
	if len(parts) == 1 {
		group = "core"
		version = parts[0]
	} else {
		group = parts[0]
		version = parts[1]
	}

	rm, _ := c.GetRestMapper()
	gvk, err := rm.KindFor(schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: kind,
	})
	if err != nil {
		return nil, err
	}
	gk := schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}
	mapping, err := rm.RESTMapping(gk, gvk.Version)
	if err != nil {
		return nil, err
	}
	return c.DynamicClient.Resource(mapping.Resource), nil
}

func (c *DynamicClient) GetRestMapper() (meta.RESTMapper, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	if c.restMapper != nil {
		return c.restMapper, nil
	}

	config, err := c.GetRESTConfig()
	if err != nil {
		return nil, err
	}

	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	c.restMapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
	return c.restMapper, err
}

func (c *DynamicClient) GetRESTConfig() (*rest.Config, error) {
	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubeConfig, nil
}

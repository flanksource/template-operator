package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-openapi/spec"
	lru "github.com/hashicorp/golang-lru"
	"github.com/pkg/errors"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
)

type SchemaCache struct {
	clientset *kubernetes.Clientset
	expire    time.Duration
	lock      *sync.Mutex
	crdClient extapi.ApiextensionsV1Interface

	resources                []*metav1.APIResourceList
	resourcesExpireTimestamp time.Time

	schema                *spec.Swagger
	schemaExpireTimestamp time.Time

	crds                []extv1.CustomResourceDefinition
	crdsExpireTimestamp time.Time

	schemaUnmarshalCache *lru.Cache

	log logr.Logger
}

func NewSchemaCache(clientset *kubernetes.Clientset, crdClient extapi.ApiextensionsV1Interface, expire time.Duration, log logr.Logger) *SchemaCache {
	schemaUnmarshalCache, _ := lru.New(100)

	sc := &SchemaCache{
		clientset:            clientset,
		crdClient:            crdClient,
		expire:               expire,
		lock:                 &sync.Mutex{},
		log:                  log,
		schemaUnmarshalCache: schemaUnmarshalCache,

		resources: nil,
	}
	return sc
}

func (sc *SchemaCache) ExpireSchema() error {
	sc.lock.Lock()
	defer sc.lock.Unlock()
	if sc.schemaExpireTimestamp.After(time.Now()) {
		sc.schemaExpireTimestamp = time.Now()
	}
	if sc.crdsExpireTimestamp.After(time.Now()) {
		sc.crdsExpireTimestamp = time.Now()
	}
	return nil
}

func (sc *SchemaCache) ExpireResources() error {
	sc.lock.Lock()
	defer sc.lock.Unlock()
	if sc.resourcesExpireTimestamp.After(time.Now()) {
		sc.resourcesExpireTimestamp = time.Now()
	}
	return nil
}

func (sc *SchemaCache) SchemaHasExpired() bool {
	return sc.schemaExpireTimestamp.Before(time.Now())
}

func (sc *SchemaCache) ResourceHasExpired() bool {
	return sc.resourcesExpireTimestamp.Before(time.Now())
}

func (sc *SchemaCache) FetchSchema() (*spec.Swagger, error) {
	sc.lock.Lock()
	defer sc.lock.Unlock()

	if sc.resources == nil || time.Now().After(sc.schemaExpireTimestamp) {
		sc.log.V(2).Info("before fetch schema")
		if err := sc.fetchAndSetSchema(); err != nil {
			return nil, errors.Wrap(err, "failed to refetch API schema")
		}
		sc.log.V(2).Info("after fetch schema")
	}

	return sc.schema, nil
}

func (sc *SchemaCache) FetchResources() ([]*metav1.APIResourceList, error) {
	sc.lock.Lock()
	defer sc.lock.Unlock()

	if sc.resources == nil || time.Now().After(sc.resourcesExpireTimestamp) {
		sc.log.V(2).Info("before fetch resources")
		if err := sc.fetchAndSetResources(); err != nil {
			return nil, errors.Wrap(err, "failed to refetch API resources")
		}
		sc.log.V(2).Info("after fetch resources")
	}
	return sc.resources, nil
}

func (sc *SchemaCache) FetchCRD() ([]extv1.CustomResourceDefinition, error) {
	sc.lock.Lock()
	defer sc.lock.Unlock()

	if sc.crds == nil || time.Now().After(sc.crdsExpireTimestamp) {
		sc.log.V(2).Info("before fetch crds")
		crds, err := sc.crdClient.CustomResourceDefinitions().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to list customresourcedefinitions")
		}
		sc.crds = crds.Items
		sc.crdsExpireTimestamp = time.Now().Add(sc.expire)
		sc.log.V(2).Info("after fetch crds")
	}

	return sc.crds, nil
}

func (sc *SchemaCache) CachedConvertSchema(gvk schema.GroupVersionKind, crd extv1.CustomResourceDefinitionVersion) (*spec.Schema, error) {
	key := fmt.Sprintf("group=%s;version=%s;kind=%s", gvk.Group, gvk.Version, gvk.Kind)

	sc.lock.Lock()
	defer sc.lock.Unlock()

	schemaI, found := sc.schemaUnmarshalCache.Get(key)
	if found {
		schema, ok := schemaI.(*spec.Schema)
		if ok {
			return schema, nil
		}
		sc.log.Info("failed to fetch schema from lru cache")
	}

	schemaBytes, err := json.Marshal(crd.Schema.OpenAPIV3Schema)
	if err != nil {
		return nil, errors.Wrap(err, "failed to encode crd schema to json")
	}

	schema := &spec.Schema{}
	if err := json.Unmarshal(schemaBytes, schema); err != nil {
		return nil, errors.Wrap(err, "failed to decode json into spec.Schema")
	}

	sc.schemaUnmarshalCache.Add(key, schema)
	return schema, nil
}

func (sc *SchemaCache) fetchAndSetSchema() error {
	bs, err := sc.clientset.RESTClient().Get().AbsPath("openapi", "v2").DoRaw(context.TODO())
	if err != nil {
		return errors.Wrap(err, "failed to fetch schema from server")
	}
	s := &spec.Swagger{}

	if err := json.Unmarshal(bs, &s); err != nil {
		return errors.Wrap(err, "failed to unmarshal openapi")
	}

	sc.schema = s
	sc.schemaExpireTimestamp = time.Now().Add(sc.expire)

	return nil
}

func (sc *SchemaCache) fetchAndSetResources() error {
	serverResources, err := sc.clientset.ServerResources()
	if err != nil {
		return errors.Wrap(err, "failed to list server resources")
	}
	sc.resources = serverResources
	sc.resourcesExpireTimestamp = time.Now().Add(sc.expire)
	return nil
}

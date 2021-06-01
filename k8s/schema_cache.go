package k8s

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-openapi/spec"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type SchemaCache struct {
	clientset *kubernetes.Clientset
	expire    time.Duration
	lock      *sync.Mutex

	resources                []*metav1.APIResourceList
	resourcesExpireTimestamp time.Time

	schema                *spec.Swagger
	schemaExpireTimestamp time.Time
	log                   logr.Logger
}

func NewSchemaCache(clientset *kubernetes.Clientset, expire time.Duration, log logr.Logger) *SchemaCache {
	sc := &SchemaCache{
		clientset: clientset,
		expire:    expire,
		lock:      &sync.Mutex{},
		log:       log,

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

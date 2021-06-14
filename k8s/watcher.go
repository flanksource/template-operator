package k8s

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/kommons"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type CallbackFunc func()

type WatcherInterface interface {
	Watch(exampleObject runtime.Object, template *templatev1.Template, cb CallbackFunc) error
}

type NullWatcher struct{}

func (w *NullWatcher) Watch(exampleObject runtime.Object, template *templatev1.Template, cb CallbackFunc) error {
	return nil
}

type Watcher struct {
	clientset *kubernetes.Clientset
	client    *kommons.Client
	mtx       *sync.Mutex
	cache     map[string]bool
}

func NewWatcher(client *kommons.Client) (WatcherInterface, error) {
	clientset, err := client.GetClientset()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get clientset")
	}

	watcher := &Watcher{
		clientset: clientset,
		client:    client,
		mtx:       &sync.Mutex{},
		cache:     map[string]bool{},
	}

	return watcher, nil
}

func (w *Watcher) Watch(exampleObject runtime.Object, template *templatev1.Template, cb CallbackFunc) error {
	cacheKey := getCacheKey(exampleObject, template)
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.cache[cacheKey] {
		return nil
	}
	w.cache[cacheKey] = true

	logger.Debugf("Deploying new watcher for object=%s", exampleObject.GetObjectKind().GroupVersionKind().Kind)

	factory := informers.NewSharedInformerFactory(w.clientset, 0)

	di, _, _, err := w.client.GetDynamicClientFor(v1.NamespaceAll, exampleObject)
	if err != nil {
		return errors.Wrap(err, "failed to get dynamic client")
	}

	informer := factory.InformerFor(exampleObject, func(i kubernetes.Interface, d time.Duration) cache.SharedIndexInformer {
		c := cache.NewSharedIndexInformer(
			&cache.ListWatch{
				ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
					return di.List(context.TODO(), options)
				},
				WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
					return di.Watch(context.TODO(), options)
				},
			},
			exampleObject,
			d,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		)
		return c
	})

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		// When a new pod gets created
		AddFunc: func(obj interface{}) {
			logger.Debugf("Received callback for object added: %v", obj)
			cb()
		},
		// When a pod gets updated
		UpdateFunc: func(oldObj interface{}, obj interface{}) {
			logger.Debugf("Received callback for object updated: %v", obj)
			cb()
		},
		// When a pod gets deleted
		DeleteFunc: func(obj interface{}) {
			logger.Debugf("Received callback for object deleted: %v", obj)
		},
	})

	stopper := make(chan struct{})
	go informer.Run(stopper)

	return nil
}

func getCacheKey(obj runtime.Object, template *templatev1.Template) string {
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	return fmt.Sprintf("kind=%s;template=%s", kind, template.Name)
}

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/kommons"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type CallbackFunc func(unstructured.Unstructured) error

type WatcherInterface interface {
	Watch(exampleObject *unstructured.Unstructured, template *templatev1.Template, cb CallbackFunc) error
}

type NullWatcher struct{}

func (w *NullWatcher) Watch(exampleObject *unstructured.Unstructured, template *templatev1.Template, cb CallbackFunc) error {
	return nil
}

type Watcher struct {
	clientset *kubernetes.Clientset
	client    *kommons.Client
	mtx       *sync.Mutex
	cache     map[string]bool
	log       logr.Logger
}

func NewWatcher(client *kommons.Client, log logr.Logger) (WatcherInterface, error) {
	clientset, err := client.GetClientset()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get clientset")
	}

	watcher := &Watcher{
		clientset: clientset,
		client:    client,
		mtx:       &sync.Mutex{},
		cache:     map[string]bool{},
		log:       log,
	}

	return watcher, nil
}

func (w *Watcher) Watch(exampleObject *unstructured.Unstructured, template *templatev1.Template, cb CallbackFunc) error {
	cacheKey := getCacheKey(exampleObject, template)
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.cache[cacheKey] {
		return nil
	}
	w.cache[cacheKey] = true

	logger.Debugf("Deploying new watcher for object=%s", exampleObject.GetObjectKind().GroupVersionKind().Kind)

	factory := informers.NewSharedInformerFactory(w.clientset, 0)

	di, err := w.getDynamicClient(exampleObject)
	if err != nil {
		return errors.Wrap(err, "failed to get dynamic client")
	}

	labelSelector, err := labelSelectorToString(template.Spec.Source.LabelSelector)
	if err != nil {
		return errors.Wrap(err, "failed to get label selector")
	}

	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: template.Spec.Source.FieldSelector,
	}

	informer := factory.InformerFor(exampleObject, func(i kubernetes.Interface, d time.Duration) cache.SharedIndexInformer {
		c := cache.NewSharedIndexInformer(
			&cache.ListWatch{
				ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
					options.FieldSelector = listOptions.FieldSelector
					options.LabelSelector = listOptions.LabelSelector
					return di.List(context.TODO(), options)
				},
				WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
					options.FieldSelector = listOptions.FieldSelector
					options.LabelSelector = listOptions.LabelSelector
					return di.Watch(context.TODO(), options)
				},
			},
			exampleObject,
			d,
			cache.Indexers{},
		)
		return c
	})

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			w.log.V(2).Info("Received callback for object added:", "object", obj)
			w.onUpdate(obj, cb)
		},
		UpdateFunc: func(oldObj interface{}, obj interface{}) {
			w.log.V(2).Info("Received callback for object updated:", "object", obj)
			w.onUpdate(obj, cb)
		},
		// When a pod gets deleted
		DeleteFunc: func(obj interface{}) {
			w.log.V(2).Info("Received callback for object deleted:", "object", obj)
		},
	})

	stopper := make(chan struct{})
	go informer.Run(stopper)

	return nil
}

func (w *Watcher) onUpdate(obj interface{}, cb CallbackFunc) {
	js, err := json.Marshal(obj)
	if err != nil {
		w.log.Error(err, "failed to marshal object for update")
		return
	}
	unstr := &unstructured.Unstructured{}
	if err := json.Unmarshal(js, &unstr.Object); err != nil {
		w.log.Error(err, "failed to unmarshal into unstructured for update")
		return
	}

	if err := cb(*unstr); err != nil {
		w.log.Error(err, "failed to run callback")
	}
}

func (w *Watcher) getDynamicClient(obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	dynamicClient, err := w.client.GetDynamicClient()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dynamic client")
	}

	mapping, err := w.client.WaitForRestMapping(obj, 2*time.Minute)
	if err != nil {
		return nil, err
	}

	if mapping.Scope == meta.RESTScopeRoot {
		return dynamicClient.Resource(mapping.Resource), nil
	}
	return dynamicClient.Resource(mapping.Resource).Namespace(v1.NamespaceAll), nil
}

func getCacheKey(obj runtime.Object, template *templatev1.Template) string {
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	return fmt.Sprintf("kind=%s;template=%s", kind, template.Name)
}

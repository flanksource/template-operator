package k8s_test

import (
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestK8s(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "K8S Suite")
}

type TestEventRecorder struct{}

func (r *TestEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	r.event(object, eventtype, reason, message, map[string]string{})
}

// Eventf is just like Event, but with Sprintf for the message field.
func (r *TestEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	msg := fmt.Sprintf(messageFmt, args...)
	r.event(object, eventtype, reason, msg, map[string]string{})
}

// AnnotatedEventf is just like eventf, but with annotations attached
func (r *TestEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	msg := fmt.Sprintf(messageFmt, args...)
	r.event(object, eventtype, reason, msg, annotations)
}

func (r *TestEventRecorder) event(object runtime.Object, eventtype, reason, message string, annotations map[string]string) {
	fmt.Printf("Received event type=%s reason=%s message='%s' on object %v\n", eventtype, reason, message, object)
}

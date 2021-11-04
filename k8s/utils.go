package k8s

import "k8s.io/apimachinery/pkg/runtime"

type NullEventRecorder struct{}

func (r *NullEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {}

func (r *NullEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
}

func (r *NullEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
}

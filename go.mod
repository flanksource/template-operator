module github.com/flanksource/template-operator

go 1.13

require (
	9fans.net/go v0.0.2 // indirect
	github.com/AlekSi/pointer v1.1.0
	github.com/flanksource/commons v1.4.0
	github.com/go-logr/logr v0.2.1
	github.com/go-logr/zapr v0.2.0
	github.com/go-test/deep v1.0.2-0.20181118220953-042da051cf31
	github.com/mitchellh/mapstructure v1.1.2
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.1
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.4.2
	github.com/tidwall/gjson v1.6.1
	gopkg.in/flanksource/yaml.v3 v3.1.1
	k8s.io/api v0.19.1
	k8s.io/apimachinery v0.19.1
	k8s.io/cli-runtime v0.19.1
	k8s.io/client-go v11.0.0+incompatible
	sigs.k8s.io/controller-runtime v0.6.3
	sigs.k8s.io/controller-tools v0.4.0 // indirect
	sigs.k8s.io/kustomize v2.0.3+incompatible
	sigs.k8s.io/yaml v1.2.0
)

replace k8s.io/client-go => k8s.io/client-go v0.19.1

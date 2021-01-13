module github.com/flanksource/template-operator

go 1.13

require (
	github.com/flanksource/commons v1.4.3
	github.com/flanksource/kommons v0.1.6
	github.com/go-logr/logr v0.3.0
	github.com/go-logr/zapr v0.3.0 // indirect
	github.com/go-openapi/jsonpointer v0.19.3
	github.com/go-openapi/spec v0.19.3
	github.com/hairyhenderson/gomplate/v3 v3.6.0
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.1
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.7.0
	github.com/sykesm/zap-logfmt v0.0.4
	github.com/tidwall/gjson v1.6.1
	github.com/zalando/postgres-operator v1.6.0
	go.uber.org/zap v1.15.0
	gopkg.in/flanksource/yaml.v3 v3.1.1
	k8s.io/api v0.19.4
	k8s.io/apiextensions-apiserver v0.19.4
	k8s.io/apimachinery v0.19.4
	k8s.io/cli-runtime v0.19.3
	k8s.io/client-go v12.0.0+incompatible
	sigs.k8s.io/controller-runtime v0.6.3
	sigs.k8s.io/kustomize v2.0.3+incompatible
	sigs.k8s.io/yaml v1.2.0
)

replace (
	github.com/Azure/go-autorest => github.com/Azure/go-autorest v13.0.0+incompatible
	google.golang.org/genproto => google.golang.org/genproto v0.0.0-20191230161307-f3c370f40bfb
	google.golang.org/grpc => google.golang.org/grpc v1.26.0
	k8s.io/client-go => k8s.io/client-go v0.19.4
)

#!/bin/bash

set -e

export KARINA_VERSION=v0.50.1
export KARINA="./karina -c test/config.yaml"
export KUBECONFIG=~/.kube/config
export DOCKER_API_VERSION=1.39

if [[ "$OSTYPE" == "linux-gnu" ]]; then
  wget -q https://github.com/flanksource/karina/releases/download/$KARINA_VERSION/karina
  chmod +x karina
elif [[ "$OSTYPE" == "darwin"* ]]; then
  wget -q https://github.com/flanksource/karina/releases/download/$KARINA_VERSION/karina_osx
  cp karina_osx karina
  chmod +x karina
else
  echo "OS $OSTYPE not supported"
  exit 1
fi

mkdir -p .bin

KUSTOMIZE=./.bin/kustomize
if [ ! -f "$KUSTOMIZE" ]; then
  curl -s "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"  | bash
  mv kustomize .bin
fi
export PATH=$(pwd)/.bin:$PATH

$KARINA ca generate --name root-ca --cert-path .certs/root-ca.crt --private-key-path .certs/root-ca.key --password foobar  --expiry 1
$KARINA ca generate --name ingress-ca --cert-path .certs/ingress-ca.crt --private-key-path .certs/ingress-ca.key --password foobar  --expiry 1
$KARINA provision kind-cluster -vvvvv

$KARINA deploy bootstrap
$KARINA deploy postgres-operator
$KARINA deploy flux
export IMG=flanksource/template-operator:v1
make docker-build
kind load docker-image $IMG --name kind-kind

make deploy

kubectl apply -f examples/postgres-operator.yml
kubectl apply -f examples/namespacerequest.yml
kubectl apply -f examples/for-each.yml
kubectl apply -f examples/when.yaml
kubectl apply -f test/fixtures/awx-operator.yml
kubectl apply -f test/fixtures/depends-on.yaml
kubectl apply -f test/fixtures/mockserver.yml
kubectl apply -f test/fixtures/git-repository.yaml

go run test/e2e.go

go test ./k8s

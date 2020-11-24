#!/bin/bash

set -e

export KARINA_VERSION=v0.21.0
export KARINA="./karina -c test/config.yaml"
export KUBECONFIG=~/.kube/config
export DOCKER_API_VERSION=1.39

if [[ "$OSTYPE" == "linux-gnu" ]]; then
  wget -q https://github.com/flanksource/karina/releases/download/$KARINA_VERSION/platform-cli
  mv platform-cli karina
  # wget -q https://github.com/flanksource/karina/releases/download/$KARINA_VERSION/karina
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

KUSTOMIZE=./bin/kustomize
if [ ! -f "$KUSTOMIZE" ]; then
  curl -s "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"  | bash
  mv kustomize .bin
fi
export PATH=$(pwd)/.bin:$PATH

$KARINA ca generate --name root-ca --cert-path .certs/root-ca.crt --private-key-path .certs/root-ca.key --password foobar  --expiry 1
$KARINA ca generate --name ingress-ca --cert-path .certs/ingress-ca.crt --private-key-path .certs/ingress-ca.key --password foobar  --expiry 1
$KARINA provision kind-cluster

$KARINA deploy crds
$KARINA deploy calico
kubectl -n kube-system set env daemonset/calico-node FELIX_IGNORELOOSERPF=true

$KARINA deploy base
$KARINA deploy stubs

export IMG=flanksource/template-operator:v1
make docker-build
kind load docker-image $IMG --name kind-kind

make deploy

go run test/e2e.go

go test ./k8s
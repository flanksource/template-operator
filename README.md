# Template Operator

<!-- markdownlint-disable-next-line MD036 -->
**Simple, reconciliation-based runtime templating**

The Template Operator is for platform engineers needing an easy and reliable way to create, copy and update kubernetes resources.

## Design principles

- **100% YAML** – `Templates` are valid YAML and IDE validation and autocomplete of k8s resources works as normal.
- **Simple** – Easy to use and quick to get started.
- **Reconciliation based** – Changes are applied quickly and resiliently (unlike webhooks) at runtime.

### Alternatives

There are alternative templating systems in use by the k8s community – each has valid use cases and noting the downsides for runtime templating is not intended as an indictment – all are excellent choices under the right conditions.

| Alternative              | Downside for templating                                  |
| ------------------------ | :------------------------------------------------------- |
| [crossplane][crossplane] | Complex due to design for infrastructure composition     |
| [kyverno][kyverno]       | Webhook based<br />Designed as a policy engine           |
| [helm][helm]             | Not 100% YAML<br />Not reconciliation based (build time) |

[crossplane]: https://crossplane.io/  "Crossplane"
[kyverno]: https://kyverno.io/  "Kyverno"
[helm]: https://helm.sh/ "Helm"

## Installation

API documentation available [here](https://pkg.go.dev/github.com/flanksource/template-operator/api/v1).

### Prerequisites

This guide assumes you have either a [kind cluster](https://kind.sigs.k8s.io/docs/user/quick-start/) or [minikube cluster](https://minikube.sigs.k8s.io/docs/start/) running, or have some other way of interacting with a cluster via [kubectl](https://kubernetes.io/docs/tasks/tools/).

### Install

```bash
export VERSION=0.2.0
# For the latest release version: https://github.com/flanksource/template-operator/releases

# Apply the operator
kubectl apply -f https://github.com/flanksource/template-operator/releases/download/v${VERSION}/operator.yml
```

## Other examples

An example replicating the functionality of <https://github.com/pusher/quack>

```yaml
apiVersion: templating.flanksource.com/v1
kind: Template
metadata:
  name: dynamic-ingress-hostnames
spec:
  onceoff: true # need to set this flag as otherwise it will trigger an endless loop appending the domain to iteself.
  namespaceSelector:
     quack.pusher.com/enabled: "true"
  objectSelector:
    - apiVersion: extensions/v1beta1
      kind: Ingress
  patches:
    - spec:
        rules:
            # append a dynamic domain specified in a configmap to the domain specified in the ingress
          - host: "{{.source.spec.rules[0].host}}.{{-   kget "cm/quack/quack-config" "data.domain" -}}"
            tls:
              hosts:
                -  "{{.source.spec.rules[0].host}}.{{-   kget "cm/quack/quack-config" "data.domain" }}"
```

An example replicating  <https://github.com/redhat-cop/namespace-configuration-operator>

```yaml
apiVersion: templating.flanksource.com/v1
kind: Template
metadata:
  name: dcp-on-demand
  namespace: kube-system
spec:
  labelSelector:
    matchLabels:
      owner: dh-dcp
  resources:
    - kind: RoleBinding
      apiVersion: rbac.authorization.k8s.io/v1
      metadata:
        name: creator
      subjects:
        - kind: Group
          name: Owner
          apiGroup: rbac.authorization.k8s.io
      roleRef:
        apiGroup: rbac.authorization.k8s.io
        kind: ClusterRole
        name: namespace-admin
```

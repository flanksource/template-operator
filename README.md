# Template Operator

<!-- markdownlint-disable MD036 -->
**Simple, reconciliation-based runtime templating**
<!-- markdownlint-enable MD036 -->

The Template Operator is for platform engineers needing an easy and reliable way to create, copy and update kubernetes resources.

## Design principles

- **100% YAML** – `Templates` are valid YAML and IDE validation and autocomplete of k8s resources works as normal.
- **Simple** – Easy to use and quick to get started.
- **Reconciliation based** – Changes are applied quickly and resiliently (unlike webhooks) at runtime.

## Further reading

This README replicates much of the content from [Simple, reconciliation-based runtime templating](/docs/template-operator-intro-part-1.md).

For further examples, see part 2 in the series: [Powering up with Custom Resource Definitions (CRDs)](/docs/template-operator-intro-part-2.md).

### Alternatives

There are alternative templating systems in use by the k8s community – each has valid use cases and noting the downsides for runtime templating is not intended as an indictment – all are excellent choices under the right conditions.

<!-- markdownlint-disable MD033 -->
| Alternative              | Downside for templating                                  |
| ------------------------ | :------------------------------------------------------- |
| [crossplane][crossplane] | Complex due to design for infrastructure composition     |
| [kyverno][kyverno]       | Webhook based<br />Designed as a policy engine           |
| [helm][helm]             | Not 100% YAML<br />Not reconciliation based (build time) |
<!-- markdownlint-enable MD033 -->

## Installation

API documentation available [here](https://pkg.go.dev/github.com/flanksource/template-operator/api/v1).

### Prerequisites

This guide assumes you have either a [kind cluster](https://kind.sigs.k8s.io/docs/user/quick-start/) or [minikube cluster](https://minikube.sigs.k8s.io/docs/start/) running, or have some other way of interacting with a cluster via [kubectl](https://kubernetes.io/docs/tasks/tools/).

### Install

```bash
export VERSION=0.4.0
# For the latest release version: https://github.com/flanksource/template-operator/releases

# Apply the operator
kubectl apply -f https://github.com/flanksource/template-operator/releases/download/v${VERSION}/operator.yml
```

Run `kubectl get pods -A` and you should see something similar to the following in your terminal output:

```bash
NAMESPACE            NAME                                                    READY
template-operator    template-operator-controller-manager-6bd8c5ff58-sz8q6   2/2
```

### Following the logs

To follow the manager logs, open a new terminal and, changing what needs to be changed, run :

```bash
kubectl logs -f --since 10m -n template-operator deploy/template-operator-controller-manager
-c  manager
```

These logs are where reconciliation successes and errors show up – and the best place to look when debugging.

## Use case: Creating resources per namespace

> *As a platform engineer, I need to quickly provision Namespaces for application teams so that they are able to spin up environments quickly.*

As organisations grow, platform teams are often tasked with creating `Namespaces` for continuous integration or for development.

To configure a `Namespace`, platform teams may need to commit or apply many boilerplate objects.

For this example, suppose you need a set of `Roles` and `RoleBindings` to automatically deploy for a `Namespace` .

### Step 1: Adding a namespace and a template

Add a `Namespace`. You might add this after applying the `Template`, but it's helpful to see that the Template Operator doesn't care when objects are applied – a feature of the reconciliation-based approach. Note the label – this tags the `Namespace` as one that should produce `RoleBindings`.

```yaml
cat <<EOF | kubectl apply -f -
kind: Namespace
apiVersion: v1
metadata:
  name: store-5678
  labels:
    # This will be used to select on later
    type: application 
EOF
```

With the `Namespace` configured, you can apply the `Template` (see inline notes).

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: templating.flanksource.com/v1
kind: Template
metadata:
  name: namespace-rolebinder-developer
  namespace: template-operator
spec:
  # The "source" field selects for the objects to monitor.
  # API docs here: https://pkg.go.dev/github.com/flanksource/template-operator/api/v1#ResourceSelector
  source: 
    # Selects for the apiVersion
    apiVersion: v1
    # Selects for the kind
    kind: Namespace
    # Selects for the label
    labelSelector:
      matchLabels:
        type: application
  # For every matched object, Template Operator will generate the listed resources.
  resources: 
  - kind: Role
    apiVersion: rbac.authorization.k8s.io/v1
    metadata:
      name: developer
      # {{.metadata.name}} comes from the source object (".").
      # Syntax is based on go text templates with gomplate functions (https://docs.gomplate.ca).
      namespace: "{{.metadata.name}}" 
    rules:
    - apiGroups: [""]
      resources: ["secrets", "pods", "pods/log", "configmaps"]
      verbs: ["get", "watch", "list"]
  - kind: RoleBinding
    apiVersion: rbac.authorization.k8s.io/v1
    metadata:
      name: developer
      namespace: "{{.metadata.name}}"
    subjects:
      - kind: Group
        name: developer
        apiGroup: rbac.authorization.k8s.io
    roleRef:
      apiGroup: rbac.authorization.k8s.io
      kind: Role 
      name: developer
EOF
```

Because of reconciliation, though the `Namespace` "store-5678"  was applied before the `Template` `namespace-rolebinder-developer`, the operator will still produce/update the required objects.

### Step 2: See results

Once the Template Operator has reconciled (you can see this if you're tailing the logs), run `kubectl get roles.rbac.authorization.k8s.io -A` to see the newly created `Role`:

```bash
NAMESPACE         NAME          CREATED AT
store-5678        developer     2021-07-16T06:30:27Z
```

Run `kubectl get rolebindings.rbac.authorization.k8s.io -A`, for the `RoleBinding`:

```yaml
NAMESPACE     NAME          ROLE            AGE
store-5678    developer     Role/developer  10s
```

### Step 3:  Adding a second namespace

Now you can apply a second `Namespace`:

```bash
cat <<EOF | kubectl apply -f -
kind: Namespace
apiVersion: v1
metadata:
  name: store-7674
  labels:
    type: application
EOF
```

### Step 4: See results

The Template Operator will create/update the resources in its next cycle. Once the Template Operator reconciles, run `kubectl get rolebindings.rbac.authorization.k8s.io -A` and you should see something like:

```bash
NAMESPACE     NAME          ROLE            AGE
store-7674    developer     Role/developer  2m
store-5678    developer     Role/developer  8s
```

And for `kubectl get roles.rbac.authorization.k8s.io -A`:

```bash
NAMESPACE         NAME          CREATED AT
store-5678        developer     2021-07-16T06:30:27Z
store-7674        developer     2021-07-16T06:33:14Z
```

And you're done! In the next example, you'll learn how to add a `Template` to copy `Secrets` across `Namespaces`.

## Use case: Copying secrets between namespaces

> *As a platform engineer, I need to automatically copy appropriate Secrets to newly created Namespaces so that application teams have access to the Secrets they need by default.*

Suppose you have a `Namespace` containing `Secrets` you want to copy to every development `Namespace`.

### Step 1: Add secrets and namespace

Apply the following manifests to set up the `Namespace` with the `Secrets`.

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Namespace
metadata:
  name: development-secrets
  labels:
    environment: development
---
apiVersion: v1
kind: Secret
metadata:
  name: development-secrets-username
  namespace: development-secrets
  labels:
    secrets.flanksource.com/label: development
stringData:
  username: rvvq6c8p272!
type: Opaque
---
apiVersion: v1
kind: Secret
metadata:
  name: development-secrets-api
  namespace: development-secrets
  labels:
    secrets.flanksource.com/label: development
stringData:
  apikey: 7jmpsscrd272jlh 
type: Opaque
EOF
```

### Step 2: Apply the template

Then add a `Template` with the [copyToNamespaces](https://pkg.go.dev/github.com/flanksource/template-operator/api/v1#CopyToNamespaces) field.

```yaml
cat <<EOF | kubectl apply -f -    
kind: Template
apiVersion: templating.flanksource.com/v1
metadata:
  name: copy-development-secrets
spec:
  source:
    apiVersion: v1
    kind: Secret 
    # selects on the Namespace label
    namespaceSelector:
      matchLabels:
        environment: development
    # selects on the Secret label
    labelSelector:
      matchLabels:
        secrets.flanksource.com/label: development 
  copyToNamespaces:
    # selects on the Namespace label 
    namespaceSelector:
      matchLabels:
        type: application 
EOF
```

### Step 3: See results

Once the Template Operator has reconciled, run `kubectl get secrets -A` to see the copied secrets:

```bash
NAMESPACE             NAME                             TYPE     DATA   AGE
store-5678            development-secrets-api          Opaque   1      3s
store-5678            development-secrets-username     Opaque   1      3s
store-7674            development-secrets-api          Opaque   1      5s
store-7674            development-secrets-username     Opaque   1      5s
```

[crossplane]: https://crossplane.io/  "Crossplane"
[kyverno]: https://kyverno.io/  "Kyverno"
[helm]: https://helm.sh/ "Helm"

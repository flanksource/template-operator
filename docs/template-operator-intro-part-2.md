---
Author: Saul Nachman & Moshe Immerman
Last updated: 22/07/2021
---

<!-- markdownlint-disable MD041 -->
*This is part 2 of a series demonstrating the Template Operator's capabilities. This post covers templating out a [coordinated deployment, service and ingress](#Use-case-Coordinated-deployment-service-and-ingress) using a CRD, as well as [making a homemade CRD](#Making-a-homemade-CRD).*
<!-- markdownlint-enable MD041 -->

# Template Operator

<!-- markdownlint-disable MD036 -->
**Powering up with Custom Resource Definitions (CRDs)**
<!-- markdownlint-enable MD036 -->

Using CRDs with the Template Operator is a powerful technique allowing platform engineers to set up complex deployments with minimal configuration.

## Prerequisites

See [the project readme](https://github.com/flanksource/template-operator/blob/v0.3.0/README.md#install) for installation instructions.

To test the deployment in the browser, set up ingress to your cluster – see, for example, the [minikube](https://kubernetes.io/docs/tasks/access-application-cluster/ingress-minikube/#create-an-ingress-resource) or [kind](https://kind.sigs.k8s.io/docs/user/ingress) docs.

## Use case: Coordinated deployment, service and ingress

*As a platform engineer, I need it to be easy for development teams to expose applications to the internet, so that they can move quickly and encounter only as much complexity as they must.*

Improving team productivity – one demand placed on platform teams – involves providing teams new capabilities while also reducing complexity.

In the example, you will use the `TutorialService` spec, which has three fields as well as the standard [metadata](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1@v0.20.4#ObjectMeta):

- `image` (required, string)
- `domain` (required, string)
- `replicas` (optional, int)

Using just these values, you can generate a properly configured deployment, service and ingress.

> **Note:** You can see how this CRD was generated in [Making a homemade CRD](#Making-a-homemade-CRD).

### Deployment, service and ingress

#### **Step 1: Apply the CRD**

Install the tutorial `TutorialService` CRD:

```bash
kubectl apply -f https://raw.githubusercontent.com/flanksource/template-operator/v0.4.0/examples/tutorial-crd.yaml
```

#### **Step 2: Create TutorialService instances** and a namespace

Apply the two `TutorialService` instances:

```yaml
cat <<EOF | kubectl apply -f -
kind: Namespace
apiVersion: v1
metadata:
  name: crd-tutorial
---
apiVersion: tutorial.tutorial/v1
kind: TutorialService
metadata:
  name: tutorial-application-1
  namespace: crd-tutorial
spec:
  image: nginx:1.21.1
  domain: test
  replicas: 2
---
apiVersion: tutorial.tutorial/v1
kind: TutorialService
metadata:
  name: tutorial-application-2
  namespace: crd-tutorial
spec:
  image: nginx:1.21.1
  domain: test
  # Notice there is no 'replicas' field specified here.
EOF
```

#### **Step 3: Apply the tutorial-service template**

Apply the template, which will reconcile using the `TutorialService` instances above:

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: templating.flanksource.com/v1
kind: Template
metadata:
  name: tutorial-service
spec:
  # The "source" field selects for the objects to monitor.
  # API docs here: https://pkg.go.dev/github.com/flanksource/template-operator/api/v1#ResourceSelector
  source:
   # Selects for the apiVersion
    apiVersion: tutorial.tutorial/v1
    # Selects for the kind – in this case TutorialService instances
    kind: TutorialService
  resources:
   # Adds a deployment
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        # {{.metadata.name}} comes from the source object (".").
        # Syntax is based on go text templates with gomplate functions (https://docs.gomplate.ca).
        name: "{{.metadata.name}}"
        namespace: "{{.metadata.namespace}}"
        labels:
          app: "{{.metadata.name}}"
      spec:
       # Notice that when replicas are not specified the default value is set to "2".
        replicas: '{{.spec.replicas | default "2"}}'
        selector:
          matchLabels:
            app: "{{.metadata.name}}"
        template:
          metadata:
            labels:
              app: "{{.metadata.name}}"
          spec:
            containers:
              - name: "{{.metadata.name}}"
               # The container image is consumed here.
                image: '{{.spec.image}}'
                imagePullPolicy: IfNotPresent
                ports:
                  - containerPort: 80
                    protocol: TCP
      # Adds a service
    - apiVersion: v1
      kind: Service
      metadata:
        name: "{{.metadata.name}}"
        namespace: "{{.metadata.namespace}}"
      spec:
        selector:
          app: "{{.metadata.name}}"
        ports:
          - port: 8080
            targetPort: 80
      # Adds an ingress
    - apiVersion: networking.k8s.io/v1
      kind: Ingress
      metadata:
        annotations:
          kubernetes.io/tls-acme: true
        name: "{{.metadata.name}}"
        namespace: "{{.metadata.namespace}}"
        labels:
          app: "{{.metadata.name}}"
      spec:
        rules:
        # The domain is consumed here
        - host: "{{.metadata.name}}.{{.spec.domain}}"
          http:
            paths:
              - pathType: Prefix
                path: /
                backend:
                  service:
                    name: "{{.metadata.name}}"
                    port:
                      number: 8080
        tls:
          - hosts:
              - '{{.metadata.name}}.{{.spec.domain}}'
            secretName: "{{.metadata.name}}-tls"
EOF
```

#### **Step 4: See results**

Once the Template Operator has reconciled (see [Part 1](#dummy-link) for how to find the logs), run:

```bash
kubectl get ingress -n crd-tutorial
```

```bash
kubectl get services -n crd-tutorial
```

```bash
kubectl get pods -n crd-tutorial
```

to see, respectively:

```bash
NAMESPACE      NAME                     CLASS    HOSTS                         ADDRESS     PORTS     AGE
crd-tutorial   tutorial-application-1   <none>   tutorial-application-1.test   localhost   80, 443   64m
crd-tutorial   tutorial-application-2   <none>   tutorial-application-2.test   localhost   80, 443   64m
```

```bash
NAME                     TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)    AGE
tutorial-application-1   ClusterIP   10.96.74.220    <none>        8080/TCP   65m
tutorial-application-2   ClusterIP   10.96.113.251   <none>        8080/TCP   65m
```

```bash
NAME                                      READY   STATUS    RESTARTS   AGE
tutorial-application-1-55fcbb44d-bhpnk    1/1     Running   0          65m
tutorial-application-1-55fcbb44d-qw9rk    1/1     Running   0          65m
tutorial-application-2-5765d6fcdd-6hz47   1/1     Running   0          65m
tutorial-application-2-5765d6fcdd-7qbx9   1/1     Running   0          65m
```

Add to your hosts file what is necessary to expose URLs `tutorial-application-1.test` and `tutorial-application-2.test`, then visit [https://tutorial-application-2.test](https://tutorial-application-2.test/) and [https://tutorial-application-2.test](https://tutorial-application-2.test/) to see the default nginx index page.

## Making a homemade CRD

### Prerequisites

There are a few options for CRD generators, such as [OperatorSDK](https://sdk.operatorframework.io/) and [kubebuilder](https://book.kubebuilder.io/quick-start.html) .

For this example, we've used `kubebuilder`, but any CRD generator or a handwritten CRD would work as well.

For `kubebuilder`, install [kubebuilder](https://book.kubebuilder.io/quick-start.html) and [go 1.13 <= version < 1.16](https://golang.org/doc/install) (for downversioning, [gvm](https://github.com/moovweb/gvm) is a decent management tool).

### Create the CRD

#### **Step 1: Scaffold the operator**

```bash
kubebuilder init --domain tutorial --repo tutorial-operator
```

#### **Step 2: Create the resource**

```yaml
kubebuilder create api --group tutorial --version v1 --kind TutorialService
```

When asked, don't create the controller.

You won't need much of what's scaffolded but will need the `api/v1` folder and the (yet to be created) `crd/bases`folder.

#### **Step 3: Modify the spec**

First you'll need to add fields to the spec.

Open  `api/v1/tutorialservice_types.go` to see something like the following code.  

The example added fields under `TutorialServiceSpec` and `TutorialServiceStatus`has been removed.

Those in `TutorialServiceSpec` are what you'll modify.

```go
package v1

import (
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TutorialServiceSpec defines the desired state of TutorialService
type TutorialServiceSpec struct {
 Image    string `json:"image"`
 Domain   string `json:"domain"`
 Replicas int    `json:"replicas,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// TutorialService is the Schema for the tutorialservices API
type TutorialService struct {
 metav1.TypeMeta   `json:",inline"`
 metav1.ObjectMeta `json:"metadata,omitempty"`
 Spec   TutorialServiceSpec   `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// TutorialServiceList contains a list of TutorialService
type TutorialServiceList struct {
 metav1.TypeMeta `json:",inline"`
 metav1.ListMeta `json:"metadata,omitempty"`
 Items           []TutorialService `json:"items"`
}

func init() {
 SchemeBuilder.Register(&TutorialService{}, &TutorialServiceList{})
}

```

#### **Step 4: Generate the CRD**

To generate the new CRD, run `make manifests` from where you set up the project. Examine your file tree to a file (`crd/bases/tutorial.tutorial_tutorialservices.yaml`) containing something like the following snippet.

 This is the same CRD used in [the example above](#Use-case-Coordinated-deployment-service-and-ingress).

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.4.1
  creationTimestamp: null
  name: tutorialservices.tutorial.tutorial
spec:
  group: tutorial.tutorial
  names:
    kind: TutorialService
    listKind: TutorialServiceList
    plural: tutorialservices
    singular: tutorialservice
  scope: Namespaced
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        description: TutorialService is the Schema for the tutorialservices API
        properties:
          apiVersion:
            description: 'APIVersion defines the versioned schema of this representation
              of an object. Servers should convert recognized schemas to the latest
              internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
            type: string
          kind:
            description: 'Kind is a string value representing the REST resource this
              object represents. Servers may infer this from the endpoint the client
              submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
            type: string
          metadata:
            type: object
          spec:
            description: TutorialServiceSpec defines the desired state of TutorialService
            properties:
              domain:
                type: string
              image:
                type: string
              replicas:
                type: integer
            # Note that the properties missing the 'omitempty' annotation are `required`.
            required:
            - domain
            - image
            type: object
        type: object
    served: true
    storage: true
    subresources:
      status: {}
status:
  acceptedNames:
    kind: ""
    plural: ""
  conditions: []
  storedVersions: []
EOF
```

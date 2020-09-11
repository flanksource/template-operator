# template-operator




An example replicating the functionality of https://github.com/pusher/quack

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

An example replicating  https://github.com/redhat-cop/namespace-configuration-operator
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

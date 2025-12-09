# hf-operator Helm chart

Install CRDs + operator:

```bash
helm install hf-operator charts/hf-operator --namespace default

helm install hf-operator charts/hf-operator \
  --set image.repository=ghcr.io/<ORG>/hf-operator \
  --set image.tag=v0.1.0 \
  --namespace default

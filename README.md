# Helm Chart Repository

This branch contains Helm charts for everything.

## Usage

Add this Helm repository:

```bash
helm repo add everything https://whale-net.github.io/everything/charts
helm repo update
```

Search for charts:

```bash
helm search repo everything
```

Install a chart:

```bash
helm install my-release everything/<chart-name>
```

## Available Charts

See the [index.yaml](https://whale-net.github.io/everything/charts/index.yaml) for all available charts and versions.

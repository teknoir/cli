# Backup Namespace
This project aims to create a full backup namespace for the Teknoir platform.

## This script require:
 * yq and jq to be installed
 * gcloud to be installed and configured
 * gsutil to be installed and configured
 * kubectl to be installed and configured
 * kubectl to be installed and configured for the contexts teknoir-dev & teknoir-prod
 * kubectl krew plugin manager with neat, ctx, and ns plugins installed
 * ansible with Teknoir plugins installed

## Backup
The backup script will create a backup of the namespace and store it in a bucket in the same project as the namespace.

What it backs up for each namespace:
- profile resource
- device resources
  - device devstudio flows.json & flows_cred.json
- devstudio resources
  - devstudio flows.json & flows_cred.json
- notebook resources
  - notebook files

The backup is stored in a bucket in the same project as the namespace with a timestamp in the name.

### Usage
```bash
$ ./backup.sh --context <context> --namespace <namespace>
```

### Example
```bash
$ ./backup.sh --context teknoir-prod --namespace teknoir-retail
```

### Backup all namespaces in a context
```bash
$ ./backup.sh --context teknoir-prod --namespace all
```

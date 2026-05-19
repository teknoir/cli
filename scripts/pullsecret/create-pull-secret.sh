#!/bin/bash

# Default values
SECRET_NAME="my-registry-secret"
NAMESPACE="default"
DOCKER_CONFIG="$HOME/.docker/config.json"
OUTPUT_FILE=""

# Function to show usage
usage() {
    echo "Usage: $0 [options]"
    echo "Options:"
    echo "  -n, --name <secret-name>   Name of the Kubernetes secret (default: $SECRET_NAME)"
    echo "  -ns, --namespace <ns>      Kubernetes namespace (default: $NAMESPACE)"
    echo "  -o, --output <file>        Output the secret to a YAML file instead of creating it in the cluster"
    echo "  -h, --help                 Show this help message"
    exit 1
}

# Parse arguments
while [[ "$#" -gt 0 ]]; do
    case $1 in
        -n|--name) SECRET_NAME="$2"; shift ;;
        -ns|--namespace) NAMESPACE="$2"; shift ;;
        -o|--output) OUTPUT_FILE="$2"; shift ;;
        -h|--help) usage ;;
        *) echo "Unknown parameter passed: $1"; usage ;;
    esac
    shift
done

# Check if kubectl is installed
if ! command -v kubectl &> /dev/null; then
    echo "Error: kubectl could not be found. Please install it to continue."
    exit 1
fi

# Check if Docker config file exists
if [ ! -f "$DOCKER_CONFIG" ]; then
    echo "Error: Docker config file not found at $DOCKER_CONFIG"
    echo "Please run 'docker login <your-registry>' first."
    exit 1
fi

if [ -n "$OUTPUT_FILE" ]; then
    echo "Generating Kubernetes pull secret '$SECRET_NAME' in namespace '$NAMESPACE' to file '$OUTPUT_FILE'..."

    # Generate the secret as yaml and save to file
    kubectl create secret generic "$SECRET_NAME" \
        --from-file=.dockerconfigjson="$DOCKER_CONFIG" \
        --type=kubernetes.io/dockerconfigjson \
        --namespace="$NAMESPACE" \
        --dry-run=client \
        -o yaml > "$OUTPUT_FILE"

    if [ $? -eq 0 ]; then
        echo "✅ Successfully generated secret to '$OUTPUT_FILE'."
    else
        echo "❌ Failed to generate the secret file."
    fi
else
    echo "Creating Kubernetes pull secret '$SECRET_NAME' in namespace '$NAMESPACE'..."

    # Create the secret directly in the cluster
    kubectl create secret generic "$SECRET_NAME" \
        --from-file=.dockerconfigjson="$DOCKER_CONFIG" \
        --type=kubernetes.io/dockerconfigjson \
        --namespace="$NAMESPACE"

    if [ $? -eq 0 ]; then
        echo "✅ Successfully created secret '$SECRET_NAME'."
        echo ""
        echo "To use it, add this to your Pod or Deployment spec:"
        echo "      imagePullSecrets:"
        echo "        - name: $SECRET_NAME"
    else
        echo "❌ Failed to create the secret."
    fi
fi
#!/bin/bash
set -e

# Script to fetch a k3s KUBECONFIG from a remote host, 
# update the server address, and help merge it locally.

REMOTE_USER="anders"
REMOTE_HOST="rtx2000-pro-bw-se.teknoir"
REMOTE_PATH="/etc/rancher/k3s/k3s.yaml"
LOCAL_TEMP_FILE=$(mktemp)
CLUSTER_NAME="${REMOTE_HOST}"

echo "--- Fetching KUBECONFIG from ${REMOTE_USER}@${REMOTE_HOST} ---"

# 1. Fetch the file via SSH (requires sudo)
ssh "${REMOTE_USER}@${REMOTE_HOST}" "sudo cat ${REMOTE_PATH}" > "${LOCAL_TEMP_FILE}"

# 2. Update the server field
# k3s default is https://127.0.0.1:6443
# We need to change it to the remote hostname.
# Using sed for portability if yq isn't available, but yq is better if installed.
if command -v yq >/dev/null 2>&1; then
    # Use yq to update the server field
    # k3s k3s.yaml usually has one cluster entry
    yq e ".clusters[0].cluster.server = \"https://${REMOTE_HOST}:6443\"" -i "${LOCAL_TEMP_FILE}"
    # Also rename the context/cluster/user to be unique if they are 'default'
    yq e ".clusters[0].name = \"${CLUSTER_NAME}\"" -i "${LOCAL_TEMP_FILE}"
    yq e ".contexts[0].name = \"${CLUSTER_NAME}\"" -i "${LOCAL_TEMP_FILE}"
    yq e ".contexts[0].context.cluster = \"${CLUSTER_NAME}\"" -i "${LOCAL_TEMP_FILE}"
    yq e ".contexts[0].context.user = \"${CLUSTER_NAME}\"" -i "${LOCAL_TEMP_FILE}"
    yq e ".users[0].name = \"${CLUSTER_NAME}\"" -i "${LOCAL_TEMP_FILE}"
else
    echo "Warning: 'yq' not found, using 'sed' to update server. Context names might still be 'default'."
    sed -i '' "s|server: https://127.0.0.1:6443|server: https://${REMOTE_HOST}:6443|g" "${LOCAL_TEMP_FILE}"
fi

echo "--- KUBECONFIG updated with server: https://${REMOTE_HOST}:6443 ---"

# 3. Inform about merging
echo ""
echo "The updated KUBECONFIG is at: ${LOCAL_TEMP_FILE}"
echo "To merge it into your main ~/.kube/config, you can use one of these methods:"
echo ""
echo "Method A: Using 'konfig' krew plugin (Recommended)"
echo "  kubectl konfig import --save ${LOCAL_TEMP_FILE}"
echo ""
echo "Method B: Manual merge"
echo "  KUBECONFIG=~/.kube/config:${LOCAL_TEMP_FILE} kubectl config view --flatten > ~/.kube/config.new"
echo "  mv ~/.kube/config.new ~/.kube/config"
echo ""
echo "If you don't have 'konfig', install it via krew:"
echo "  kubectl krew install konfig"
echo ""

# Cleanup handled by user or they can use the file
# rm "${LOCAL_TEMP_FILE}"

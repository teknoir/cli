#!/bin/sh
set -e

POSITIONAL=()
while [[ $# -gt 0 ]]
do
key="$1"

case $key in
    -n|--namespace)
    NAMESPACE="$2"
    shift # past argument
    shift # past value
    ;;
    -d|--device)
    DEVICE="$2"
    shift # past argument
    shift # past value
    ;;
    -c|--context)
    CONTEXT=$2
    shift # past argument
    shift # past value
    ;;
    -o|--output)
    OUTPUT_FILE="$2"
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "Usage: $0 -c <context> -n <namespace> -d <device> [-o <output_file>]"
    echo "Generates a standalone SSH script with embedded credentials."
    exit 0
    ;;
esac
done

if [[ -z "$NAMESPACE" || -z "$DEVICE" ]]; then
    echo "Error: Namespace and Device are required."
    exit 1
fi

if [[ -z "$OUTPUT_FILE" ]]; then
    OUTPUT_FILE="ssh_${DEVICE}.sh"
fi

DEVICE_RESOURCE="device"

if [[ -z "${CONTEXT}" ]]; then
  DEVICE_MANIFEST="$(kubectl -n $NAMESPACE get $DEVICE_RESOURCE $DEVICE -o yaml)"
else
  DEVICE_MANIFEST="$(kubectl --context ${CONTEXT} -n $NAMESPACE get $DEVICE_RESOURCE $DEVICE -o yaml)"
fi

USERNAME=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.username - | base64 --decode -i -)
RSA_PRIVATE_KEY_B64=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.rsa_private -)
REMOTE_ACCESS_ACTIVE=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .subresources.status.remote_access.active -)
REMOTE_ACCESS_PORT=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .subresources.status.remote_access.port -)

if ! [[ ${REMOTE_ACCESS_ACTIVE} == true ]] ; then
   echo "error: The device ${DEVICE}, has not enabled remote access!" >&2
   exit 1
fi

case "${CONTEXT}" in
  *teknoir-dev*)
    DOMAIN="teknoir.dev"
    ;;
  *teknoir-poc-eks*)
    DOMAIN="teknoir.online"
    ;;
  *r415*)
    DOMAIN="teknoir.cloud"
    ;;
  *)
    # Defaulting to teknoir.cloud if unknown, or try to infer from context
    DOMAIN="teknoir.cloud"
    ;;
esac

DEADENDUSER='teknoir'
DEADENDHOST="deadend-${NAMESPACE}.${DOMAIN}"
DEADENDPORT='2222'

cat <<EOF > "${OUTPUT_FILE}"
#!/bin/sh
# Standalone SSH script for device: ${DEVICE}
# Generated on $(date)

set -e

USERNAME='${USERNAME}'
RSA_PRIVATE_KEY_B64='${RSA_PRIVATE_KEY_B64}'
REMOTE_ACCESS_PORT='${REMOTE_ACCESS_PORT}'
DEADENDUSER='${DEADENDUSER}'
DEADENDHOST='${DEADENDHOST}'
DEADENDPORT='${DEADENDPORT}'

RSA_KEY_FILE=\$(mktemp -t "ssh_${DEVICE}")
echo "\${RSA_PRIVATE_KEY_B64}" | base64 --decode -i - > "\${RSA_KEY_FILE}"
chmod 600 "\${RSA_KEY_FILE}"

trap "rm -f \${RSA_KEY_FILE}" EXIT INT TERM ERR

echo "Connecting to ${DEVICE} via \${DEADENDHOST}..."

PROXY_PROXY_CMD="openssl s_client -quiet -connect \${DEADENDHOST}:\${DEADENDPORT} -servername \${DEADENDHOST}"
PROXY_CMD="ssh -o ProxyCommand='\${PROXY_PROXY_CMD}' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i \${RSA_KEY_FILE} -N -W %h:%p \${DEADENDUSER}@\${DEADENDHOST} -p \${DEADENDPORT}"

ssh -o "ProxyCommand=\${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i \${RSA_KEY_FILE} \${USERNAME}@127.0.0.1 -p \${REMOTE_ACCESS_PORT}
EOF

chmod +x "${OUTPUT_FILE}"
echo "Generated standalone script: ${OUTPUT_FILE}"

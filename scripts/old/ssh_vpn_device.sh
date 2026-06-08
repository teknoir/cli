#!/bin/sh
set -e

POSITIONAL=()
ROUTE=""
LOCAL_TUN=5
REMOTE_TUN=5
LOCAL_IP="10.255.255.1"
REMOTE_IP="10.255.255.2"

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
    -r|--route)
    ROUTE="$2"
    shift # past argument
    shift # past value
    ;;
    --local-tun)
    LOCAL_TUN="$2"
    shift # past argument
    shift # past value
    ;;
    --remote-tun)
    REMOTE_TUN="$2"
    shift # past argument
    shift # past value
    ;;
    --local-ip)
    LOCAL_IP="$2"
    shift # past argument
    shift # past value
    ;;
    --remote-ip)
    REMOTE_IP="$2"
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -c(--context) <context> -n(--namespace) <namespace> -d(--device) <device-name> [-r(--route) <subnet>] [--local-tun <num>] [--remote-tun <num>] [--local-ip <ip>] [--remote-ip <ip>]"
    echo ""
    echo "Example: $0 -c teknoir-prod -n teknoir-ai -d my-device -r 192.168.1.0/24"
    exit 0
    ;;
esac
done

DEVICE_RESOURCE="device"

if [[ -z "${CONTEXT}" ]]; then
  export DEVICE_MANIFEST="$(kubectl -n $NAMESPACE get $DEVICE_RESOURCE $DEVICE -o yaml)"
else
  export DEVICE_MANIFEST="$(kubectl --context ${CONTEXT} -n $NAMESPACE get $DEVICE_RESOURCE $DEVICE -o yaml)"
fi

USERNAME=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.username - | base64 --decode -i -)
PASSWORD=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.userpassword - | base64 --decode -i -)

REMOTE_ACCESS_ACTIVE=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .subresources.status.remote_access.active -)
REMOTE_ACCESS_PORT=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .subresources.status.remote_access.port -)

if ! [[ ${REMOTE_ACCESS_ACTIVE} == true ]] ; then
   echo "error: The device ${DEVICE}, has not enabled remote access, please go to the GUI and enable remote access for the device!" >&2
   exit 1
fi

RSA_KEY_FILE=$(mktemp -t "$(basename $0)")
printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.rsa_private - | base64 --decode -i - | tee ${RSA_KEY_FILE} >/dev/null


DOMAIN="teknoir.online"
DEADENDUSER='teknoir'
DEADENDHOST="deadend-${NAMESPACE}.${DOMAIN}"
DEADENDPORT='2222'

echo "NAMESPACE             = ${NAMESPACE}"
echo "DEVICE                = ${DEVICE}"
echo "USERNAME              = ${USERNAME}"
echo "REMOTE_ACCESS_ACTIVE  = ${REMOTE_ACCESS_ACTIVE}"
echo "REMOTE_ACCESS_PORT    = ${REMOTE_ACCESS_PORT}"
echo "DEADENDHOST           = ${DEADENDHOST}"
echo "TUNNEL CONFIG         = local tun${LOCAL_TUN} (${LOCAL_IP}) -> remote tun${REMOTE_TUN} (${REMOTE_IP})"

if [ -n "$ROUTE" ]; then
    echo "ROUTE                 = ${ROUTE} via ${REMOTE_IP}"
fi

PROXY_PROXY_CMD="ncat --ssl ${DEADENDHOST} ${DEADENDPORT}"
PROXY_CMD="ssh -o ProxyCommand='${PROXY_PROXY_CMD}' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} -N -W %h:%p ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}"

trap cleanup INT TERM EXIT
SSH_PID=""

cleanup() {
    set +e
    echo ""
    echo "Cleaning up VPN Tunnel..."
    if [ -n "$ROUTE" ]; then
        if [ "$(uname)" = "Darwin" ]; then
            sudo route delete -net ${ROUTE} ${REMOTE_IP} >/dev/null 2>&1
        else
            sudo ip route del ${ROUTE} via ${REMOTE_IP} >/dev/null 2>&1
        fi
    fi
    if [ -n "$LOCAL_IFACE" ]; then
        if [ "$(uname)" = "Darwin" ]; then
            sudo ifconfig ${LOCAL_IFACE} down >/dev/null 2>&1
        else
            sudo ip link set dev ${LOCAL_IFACE} down >/dev/null 2>&1
        fi
    fi
    if [ -n "$SSH_PID" ]; then
        sudo kill $SSH_PID >/dev/null 2>&1 || kill $SSH_PID >/dev/null 2>&1
    fi
    if [ -n "$SUDO_KEEP_ALIVE_PID" ]; then
        kill $SUDO_KEEP_ALIVE_PID >/dev/null 2>&1 || true
    fi
    if [ -f "$RSA_KEY_FILE" ]; then
        rm -f ${RSA_KEY_FILE}
    fi
    echo "Cleanup complete."
}

echo ""
echo "=========================================================="
echo "Starting SSH tunnel."
echo "This requires local root (sudo) to create the tun interface."
echo "You may be prompted for your local password now."
echo "=========================================================="

# Authenticate sudo upfront so the background process doesn't get suspended
sudo -v

# Keep-alive: update user's timestamp for sudo
while true; do sudo -n true; sleep 60; kill -0 "$$" || exit; done 2>/dev/null &
SUDO_KEEP_ALIVE_PID=$!

if [ "$(uname)" = "Darwin" ]; then
    SSH_TUN_ARGS="any:${REMOTE_TUN}"
    EXISTING_UTUNS=$(ifconfig -l | tr ' ' '\n' | grep '^utun')
else
    SSH_TUN_ARGS="${LOCAL_TUN}:${REMOTE_TUN}"
    LOCAL_IFACE="tun${LOCAL_TUN}"
fi

echo "Preparing remote device for tunnel (setting permissions and sshd_config)..."
ssh -o "ProxyCommand=${PROXY_CMD}" \
    -o 'UserKnownHostsFile=/dev/null' \
    -o 'StrictHostKeyChecking=no' \
    -i ${RSA_KEY_FILE} \
    ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT} \
    "sudo -S bash -c '
        ip tuntap add dev tun${REMOTE_TUN} mode tun user ${USERNAME} 2>/dev/null || true
        if ! grep -q \"^PermitTunnel yes\" /etc/ssh/sshd_config && ! grep -q \"^PermitTunnel point-to-point\" /etc/ssh/sshd_config; then
            echo \"Enabling PermitTunnel in sshd_config...\"
            sed -i \"s/^#*PermitTunnel.*/PermitTunnel yes/\" /etc/ssh/sshd_config
            systemctl reload ssh || systemctl reload sshd || service ssh reload || true
        fi
    '" <<< "${PASSWORD}"

# Launch SSH in background using sudo so it can create the local tun device.
# The remote side executes a bash script to configure the remote tun, routing, and firewall,
# then waits for termination and cleans up iptables.
sudo ssh -o "ProxyCommand=${PROXY_CMD}" \
         -o 'UserKnownHostsFile=/dev/null' \
         -o 'StrictHostKeyChecking=no' \
         -o 'ServerAliveInterval=60' \
         -o 'ExitOnForwardFailure=yes' \
         -w ${SSH_TUN_ARGS} \
         -i ${RSA_KEY_FILE} \
         ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT} \
         "sudo -S bash -c ' \
             ip link set dev tun${REMOTE_TUN} up && \
             ip addr add ${REMOTE_IP}/32 peer ${LOCAL_IP} dev tun${REMOTE_TUN} && \
             sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 && \
             iptables -t nat -A POSTROUTING -s ${LOCAL_IP}/32 -j MASQUERADE && \
             echo \"Remote tunnel ready. Waiting for connection...\" && \
             trap \"iptables -t nat -D POSTROUTING -s ${LOCAL_IP}/32 -j MASQUERADE; exit\" EXIT TERM INT HUP; \
             while true; do sleep 3600 & wait \$!; done \
         '" <<< "${PASSWORD}" &

SSH_PID=$!

echo "Waiting for SSH connection and remote tun initialization..."

# Wait for the local tunnel interface to appear (up to 30 seconds)
interface_ready=false
for i in {1..30}; do
    # Check if process died
    if ! sudo kill -0 $SSH_PID >/dev/null 2>&1 && ! kill -0 $SSH_PID >/dev/null 2>&1; then
        echo "Error: SSH tunnel process died unexpectedly."
        exit 1
    fi
    
    if [ "$(uname)" = "Darwin" ]; then
        CURRENT_UTUNS=$(ifconfig -l | tr ' ' '\n' | grep '^utun')
        for utun in $CURRENT_UTUNS; do
            found=false
            for ext in $EXISTING_UTUNS; do
                if [ "$utun" = "$ext" ]; then
                    found=true
                    break
                fi
            done
            if [ "$found" = false ]; then
                LOCAL_IFACE="$utun"
                interface_ready=true
                break
            fi
        done
        if [ "$interface_ready" = true ]; then
            break
        fi
    else
        if ip link show tun${LOCAL_TUN} >/dev/null 2>&1; then
            interface_ready=true
            break
        fi
    fi
    sleep 1
done

if [ "$interface_ready" = false ]; then
    echo "Error: Local tunnel interface was not created within 30 seconds."
    echo "This might be due to missing tun kernel module, or SSH failed to establish the connection."
    exit 1
fi

echo "Configuring local tunnel (${LOCAL_IFACE})..."
if [ "$(uname)" = "Darwin" ]; then
    sudo ifconfig ${LOCAL_IFACE} ${LOCAL_IP} ${REMOTE_IP} up
else
    sudo ip link set dev ${LOCAL_IFACE} up
    sudo ip addr add ${LOCAL_IP}/32 peer ${REMOTE_IP} dev ${LOCAL_IFACE}
fi

if [ -n "$ROUTE" ]; then
    echo "Adding route for ${ROUTE} via ${REMOTE_IP}..."
    if [ "$(uname)" = "Darwin" ]; then
        sudo route add -net ${ROUTE} ${REMOTE_IP}
    else
        sudo ip route add ${ROUTE} via ${REMOTE_IP}
    fi
fi

echo ""
echo "=========================================================="
echo "VPN Tunnel established successfully!"
echo "Local IP:  ${LOCAL_IP} (${LOCAL_IFACE})"
echo "Remote IP: ${REMOTE_IP} (tun${REMOTE_TUN})"
if [ -n "$ROUTE" ]; then
    echo "Routed:    ${ROUTE} -> ${REMOTE_IP}"
fi
echo "Press Ctrl+C to disconnect and clean up."
echo "=========================================================="

wait $SSH_PID

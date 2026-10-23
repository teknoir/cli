#!/bin/sh
set -e
set -o noglob
#set -x
umask 027

# --- helper functions ---
info() { echo '[INFO] ' "$@"; }
warn() { echo '[WARN] ' "$@" >&2; }
fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

# Exit if not running as root
[ $(id -u) -eq 0 ] || fatal 'You must run this script as root'

DOWNLOADER=
# --- download ---
download() {
    [ $# -eq 2 ] || fatal 'download needs exactly 2 arguments'

    case $DOWNLOADER in
        curl)
            if [ "${OS_BUILD}" ] || [ "${INSECURE}" ]; then
                info "Running installation without verifying ssl certs on URLs"
                $SUDO curl --insecure -o $1 -sSfL $2
            else
                $SUDO curl -o $1 -sSfL $2
            fi
            ;;
        wget)
            $SUDO wget -qO $1 $2
            ;;
        *)
            fatal "Incorrect executable '$DOWNLOADER'"
            ;;
    esac

    # Abort if download command failed
    [ $? -eq 0 ] || fatal 'Download failed'
}

# --- verify existence of network downloader executable ---
verify_downloader() {
    # Return failure if it doesn't exist or is no executable
    [ -x "$(command -v $1)" ] || return 1

    # Set verified executable as our downloader program and return success
    DOWNLOADER=$1
    return 0
}

verify_downloader curl || verify_downloader wget || fatal 'Can not find curl or wget for downloading files'


# This is the drop in device bootstrap script for the Teknoir platform.
# It should work for most arm32/arm64/amd64 devices running Debian or Ubuntu.

# Usage:
#   TBD
#
# Environment variables:
#   OS_BUILD=true (default: false, for OS builds)
#   INSECURE=true (default: false, for OS builds, or if you want to run without ssl cert verification)
#   INSTALL_WATCHDOG=true (default: false, enable or disable the teknoir watchdog service)
#   USE_DOCKER=true (default: false, enable the use of docker as container runtime for k3sm inly works for k3s < 1.24)
#   CREATE_USER=false (default: ask, create a user for the device without asking)


TMP=$(mktemp -dt)
# --- cleanup/error trap ---
error() {
  local code="$1"
  echo "[ERROR] Error: exiting with status ${code}"
  exit "${code}"
}
cleanup() {
  rm -rf "$TMP"
  [ $? -eq 0 ] && exit
  error "$?"
}
trap 'cleanup' EXIT

cd "$TMP"
info "Teknoir bootstrapping...${TMP}"

_DOMAIN="example.test"
_DEVICE_ID="golden-device"
_NAMESPACE="golden-ns"
_FIRST_USER_NAME="golden-user"
_FIRST_USER_PASS="TEST-ONLY-password"
_FIRST_USER_KEY="ssh-ed25519 TEST-ONLY-ssh-public golden@example.test"
_RSA_PUBLIC="TEST-ONLY-rsa-public
TEST-ONLY-rsa-public-line2"
_RSA_PRIVATE="TEST-ONLY-rsa-private
TEST-ONLY-rsa-private-line2"
_K3S_TOKEN="TEST-ONLY-k3s-token"


info "Install device specific keys"
export CONFIG_PATH=/etc/teknoir
$SUDO mkdir -p ${CONFIG_PATH}
$SUDO mkdir -p /toe_conf && $SUDO rm -rf /toe_conf && $SUDO ln -s ${CONFIG_PATH}/ /toe_conf # For backward compatibility
download ${CONFIG_PATH}/roots.pem https://pki.goog/roots.pem
$SUDO chmod 440 ${CONFIG_PATH}/roots.pem
$SUDO chown 65532:root ${CONFIG_PATH}/roots.pem
$SUDO tee ${CONFIG_PATH}/rsa_private.pem > /dev/null << EOL
${_RSA_PRIVATE}
EOL
$SUDO chmod 440 ${CONFIG_PATH}/rsa_private.pem
$SUDO chown 65532:root ${CONFIG_PATH}/rsa_private.pem
$SUDO tee ${CONFIG_PATH}/rsa_public.pem > /dev/null << EOL
${_RSA_PUBLIC}
EOL
$SUDO chmod 444 ${CONFIG_PATH}/rsa_public.pem
$SUDO chown 65532:root ${CONFIG_PATH}/rsa_public.pem
$SUDO chgrp 65532 /etc/teknoir 2>/dev/null || true

info "Install Rancher K3s"
mkdir -p /opt/k3s
export K3S_CONFIG_PATH=/etc/rancher/k3s
$SUDO mkdir -p ${K3S_CONFIG_PATH}
$SUDO tee ${K3S_CONFIG_PATH}/config.yaml > /dev/null << EOL
disable:
  - traefik
node-name: ${_DEVICE_ID}
EOL

# The registry pull secret for teknoir-registry-secret (tnctl bootstrap --docker-secret). Without one,
# a re-run keeps the credential the device already has: teknoir-registry-secret, or
# artifact-registry-secret from older installers (the default ServiceAccounts now point at
# teknoir-registry-secret only), skipping an empty {"auths":{}}. A first run gets an empty but valid
# config: an empty .dockerconfigjson is invalid JSON and makes k3s reject the whole Teknoir manifest.
_TN_EMPTY_DOCKER_SECRET=eyJhdXRocyI6e319 # {"auths":{}}
_TN_DOCKER_SECRET="TEST-ONLY-docker-secret"
if [ -z "${_TN_DOCKER_SECRET}" ]; then
    for _tn_s in teknoir-registry-secret artifact-registry-secret; do
        _TN_DOCKER_SECRET=$($SUDO k3s kubectl -n default get secret "${_tn_s}" -o jsonpath='{.data.\.dockerconfigjson}' 2>/dev/null || true)
        if [ -n "${_TN_DOCKER_SECRET}" ] && [ "${_TN_DOCKER_SECRET}" != "${_TN_EMPTY_DOCKER_SECRET}" ]; then
            info "Keeping the registry pull secret the device already has (${_tn_s})"
            break
        fi
        _TN_DOCKER_SECRET=
    done
    [ -n "${_TN_DOCKER_SECRET}" ] || _TN_DOCKER_SECRET=${_TN_EMPTY_DOCKER_SECRET}
    unset _tn_s
fi
info "Install Teknoir Manifests"
$SUDO mkdir -p /var/lib/rancher/k3s/server/manifests
$SUDO tee /var/lib/rancher/k3s/server/manifests/teknoir.yaml > /dev/null << EOL
---
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: nvidia
handler: nvidia

---
apiVersion: v1
kind: Secret
metadata:
  name: teknoir-registry-secret
  namespace: kube-system
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: "${_TN_DOCKER_SECRET}"

---
apiVersion: v1
kind: Secret
metadata:
  name: teknoir-registry-secret
  namespace: default
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: "${_TN_DOCKER_SECRET}"

---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: default
  namespace: default
imagePullSecrets:
- name: teknoir-registry-secret

---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: default
  namespace: kube-system
imagePullSecrets:
- name: teknoir-registry-secret

---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: high-priority-mqtt
  namespace: kube-system
value: 900000000
globalDefault: false
description: "This priority class should be used for mqtt service pods only."

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mqtt
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mqtt
  template:
    metadata:
      labels:
        app: mqtt
    spec:
      priorityClassName: high-priority-mqtt
      containers:
        - name: mqtt
          image: emqx/emqx-edge:latest
          imagePullPolicy: Always
          env:
           - name: EMQX_MQTT__MAX_PACKET_SIZE
             value: 3MB
          ports:
            - containerPort: 1883
            - containerPort: 18083
          resources:
            requests:
              memory: "128Mi"
              cpu: "150m"
            limits:
              memory: "512Mi"
              cpu: "1000m"

---
apiVersion: v1
kind: Service
metadata:
  name: mqtt
  namespace: kube-system
spec:
  type: NodePort
  ports:
    - nodePort: 31883
      port: 1883
      protocol: TCP
      targetPort: 1883
  selector:
    app: mqtt

---
apiVersion: v1
kind: Service
metadata:
  name: mqtt-dashboard
  namespace: kube-system
spec:
  type: NodePort
  selector:
    app: mqtt
  ports:
    - nodePort: 31083
      port: 18083
      protocol: TCP
      targetPort: 18083

---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: high-priority-toe
  namespace: kube-system
value: 1000000000
globalDefault: false
description: "This priority class should be used for toe service pods only."

---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.12.0
  name: toes.teknoir.org
spec:
  group: teknoir.org
  names:
    kind: TOE
    listKind: TOEList
    plural: toes
    singular: toe
  scope: Cluster
  versions:
    - name: v1
      schema:
        openAPIV3Schema:
          properties:
            apiVersion:
              type: string
            kind:
              type: string
            metadata:
              type: object
            spec:
              properties:
                config:
                  properties:
                    apiVersion:
                      type: string
                    items:
                      items:
                        properties:
                          apiVersion:
                            type: string
                          binaryData:
                            additionalProperties:
                              format: byte
                              type: string
                            type: object
                          data:
                            additionalProperties:
                              type: string
                            type: object
                          kind:
                            type: string
                          metadata:
                            properties:
                              annotations:
                                additionalProperties:
                                  type: string
                                type: object
                              creationTimestamp:
                                format: date-time
                                type: string
                              deletionGracePeriodSeconds:
                                format: int64
                                type: integer
                              deletionTimestamp:
                                format: date-time
                                type: string
                              finalizers:
                                items:
                                  type: string
                                type: array
                              generateName:
                                type: string
                              generation:
                                format: int64
                                type: integer
                              labels:
                                additionalProperties:
                                  type: string
                                type: object
                              managedFields:
                                items:
                                  properties:
                                    apiVersion:
                                      type: string
                                    fieldsType:
                                      type: string
                                    fieldsV1:
                                      type: object
                                    manager:
                                      type: string
                                    operation:
                                      type: string
                                    subresource:
                                      type: string
                                    time:
                                      format: date-time
                                      type: string
                                  type: object
                                type: array
                              name:
                                type: string
                              namespace:
                                type: string
                              ownerReferences:
                                items:
                                  properties:
                                    apiVersion:
                                      type: string
                                    blockOwnerDeletion:
                                      type: boolean
                                    controller:
                                      type: boolean
                                    kind:
                                      type: string
                                    name:
                                      type: string
                                    uid:
                                      type: string
                                  required:
                                    - apiVersion
                                    - kind
                                    - name
                                    - uid
                                  type: object
                                  x-kubernetes-map-type: atomic
                                type: array
                              resourceVersion:
                                type: string
                              selfLink:
                                type: string
                              uid:
                                type: string
                            type: object
                          spec:
                            x-kubernetes-preserve-unknown-fields: true
                          status:
                            x-kubernetes-preserve-unknown-fields: true
                          type:
                            type: string
                        required:
                          - metadata
                        type: object
                        x-kubernetes-preserve-unknown-fields: true
                      type: array
                    kind:
                      type: string
                    metadata:
                      properties:
                        continue:
                          type: string
                        remainingItemCount:
                          format: int64
                          type: integer
                        resourceVersion:
                          type: string
                        selfLink:
                          type: string
                      type: object
                  required:
                    - items
                  type: object
                  x-kubernetes-preserve-unknown-fields: true
              type: object
            status:
              properties:
                last_applied_config:
                  properties:
                    apiVersion:
                      type: string
                    items:
                      items:
                        properties:
                          apiVersion:
                            type: string
                          binaryData:
                            additionalProperties:
                              format: byte
                              type: string
                            type: object
                          data:
                            additionalProperties:
                              type: string
                            type: object
                          kind:
                            type: string
                          metadata:
                            properties:
                              annotations:
                                additionalProperties:
                                  type: string
                                type: object
                              creationTimestamp:
                                format: date-time
                                type: string
                              deletionGracePeriodSeconds:
                                format: int64
                                type: integer
                              deletionTimestamp:
                                format: date-time
                                type: string
                              finalizers:
                                items:
                                  type: string
                                type: array
                              generateName:
                                type: string
                              generation:
                                format: int64
                                type: integer
                              labels:
                                additionalProperties:
                                  type: string
                                type: object
                              managedFields:
                                items:
                                  properties:
                                    apiVersion:
                                      type: string
                                    fieldsType:
                                      type: string
                                    fieldsV1:
                                      type: object
                                    manager:
                                      type: string
                                    operation:
                                      type: string
                                    subresource:
                                      type: string
                                    time:
                                      format: date-time
                                      type: string
                                  type: object
                                type: array
                              name:
                                type: string
                              namespace:
                                type: string
                              ownerReferences:
                                items:
                                  properties:
                                    apiVersion:
                                      type: string
                                    blockOwnerDeletion:
                                      type: boolean
                                    controller:
                                      type: boolean
                                    kind:
                                      type: string
                                    name:
                                      type: string
                                    uid:
                                      type: string
                                  required:
                                    - apiVersion
                                    - kind
                                    - name
                                    - uid
                                  type: object
                                  x-kubernetes-map-type: atomic
                                type: array
                              resourceVersion:
                                type: string
                              selfLink:
                                type: string
                              uid:
                                type: string
                            type: object
                          spec:
                            x-kubernetes-preserve-unknown-fields: true
                          status:
                            x-kubernetes-preserve-unknown-fields: true
                          type:
                            type: string
                        required:
                          - metadata
                        type: object
                        x-kubernetes-preserve-unknown-fields: true
                      type: array
                    kind:
                      type: string
                    metadata:
                      properties:
                        continue:
                          type: string
                        remainingItemCount:
                          format: int64
                          type: integer
                        resourceVersion:
                          type: string
                        selfLink:
                          type: string
                      type: object
                  required:
                    - items
                  type: object
                  x-kubernetes-preserve-unknown-fields: true
              type: object
          type: object
      served: true
      storage: true
      subresources:
        status: {}

---
apiVersion: v1
kind: ServiceAccount
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: controller-manager-sa
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: serviceaccount
    app.kubernetes.io/part-of: teknoir
  name: teknoir-controller-manager
  namespace: kube-system
#imagePullSecrets:
#- name: teknoir-registry-secret

---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: leader-election-role
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: role
    app.kubernetes.io/part-of: teknoir
  name: teknoir-leader-election-role
  namespace: kube-system
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  verbs:
  - get
  - list
  - watch
  - create
  - update
  - patch
  - delete
- apiGroups:
  - coordination.k8s.io
  resources:
  - leases
  verbs:
  - get
  - list
  - watch
  - create
  - update
  - patch
  - delete
- apiGroups:
  - ""
  resources:
  - events
  verbs:
  - create
  - patch

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: teknoir-manager-role
rules:
- apiGroups:
  - teknoir.org
  resources:
  - toes
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - teknoir.org
  resources:
  - toes/finalizers
  verbs:
  - update
- apiGroups:
  - teknoir.org
  resources:
  - toes/status
  verbs:
  - get
  - patch
  - update

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/component: kube-rbac-proxy
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: metrics-reader
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrole
    app.kubernetes.io/part-of: teknoir
  name: teknoir-metrics-reader
rules:
- nonResourceURLs:
  - /metrics
  verbs:
  - get

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/component: kube-rbac-proxy
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: proxy-role
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrole
    app.kubernetes.io/part-of: teknoir
  name: teknoir-proxy-role
rules:
- apiGroups:
  - authentication.k8s.io
  resources:
  - tokenreviews
  verbs:
  - create
- apiGroups:
  - authorization.k8s.io
  resources:
  - subjectaccessreviews
  verbs:
  - create

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: manager-rolebinding
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrolebinding
    app.kubernetes.io/part-of: teknoir
  name: teknoir-admin-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: teknoir-controller-manager
  namespace: kube-system

---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: leader-election-rolebinding
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: rolebinding
    app.kubernetes.io/part-of: teknoir
  name: teknoir-leader-election-rolebinding
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: teknoir-leader-election-role
subjects:
- kind: ServiceAccount
  name: teknoir-controller-manager
  namespace: kube-system

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: manager-rolebinding
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrolebinding
    app.kubernetes.io/part-of: teknoir
  name: teknoir-manager-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: teknoir-manager-role
subjects:
- kind: ServiceAccount
  name: teknoir-controller-manager
  namespace: kube-system

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  labels:
    app.kubernetes.io/component: kube-rbac-proxy
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: proxy-rolebinding
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrolebinding
    app.kubernetes.io/part-of: teknoir
  name: teknoir-proxy-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: teknoir-proxy-role
subjects:
- kind: ServiceAccount
  name: teknoir-controller-manager
  namespace: kube-system

---
apiVersion: v1
kind: Service
metadata:
  labels:
    app.kubernetes.io/component: kube-rbac-proxy
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: controller-manager-metrics-service
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: service
    app.kubernetes.io/part-of: teknoir
    control-plane: controller-manager
  name: toe-metrics-service
  namespace: kube-system
spec:
  ports:
  - name: https
    port: 8443
    protocol: TCP
    targetPort: https
  selector:
    control-plane: controller-manager

---
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app.kubernetes.io/component: manager
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: controller-manager
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: deployment
    app.kubernetes.io/part-of: teknoir
    control-plane: controller-manager
  name: toe
  namespace: kube-system
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      control-plane: controller-manager
  template:
    metadata:
      annotations:
        kubectl.kubernetes.io/default-container: manager
      labels:
        teknoir.org/app: toe
        app: toe
        control-plane: controller-manager
    spec:
      # TOE mounts the device keys from hostPath /etc/teknoir and signs its MQTT JWT with them, so
      # it must run on the k3s server node (the device identity). k3s labels server nodes with
      # node-role.kubernetes.io/control-plane=true; joined agent nodes do not carry it.
      # Keep nodeSelector on the line right after priorityClassName: playbooks/toe-image.yml in the
      # ansible repo recognises the pin only there.
      priorityClassName: high-priority-toe
      nodeSelector:
        node-role.kubernetes.io/control-plane: "true"
      containers:
      - args:
        - --health-probe-bind-address=:8081
        - --metrics-bind-address=127.0.0.1:8080
        env:
        - name: CLIENT_ID
          value: "golden-device"
        - name: TOE_PROJECT
          value: "teknoir"
        - name: TOE_DOMAIN
          value: "example.test"
        - name: TOE_IOT_REGISTRY
          value: "golden-ns"
        - name: TOE_DEVICE
          value: "golden-device"
        - name: TOE_CA_CERT
          value: "/etc/teknoir/roots.pem"
        - name: TOE_PRIVATE_KEY
          value: "/etc/teknoir/rsa_private.pem"
        volumeMounts:
        - name: toe-volume
          mountPath: /etc/teknoir
        # The TOE image is pinned by digest: a moving tag with imagePullPolicy: Always can turn a
        # changed or unpullable image into a watchdog reboot loop. Bump it together with the robots
        # (ansible playbooks/toe-image.yml). The digest is the multi-arch (amd64 + arm64) index
        # copied from ghcr.io/teknoir/toe-app:sha-4c366dae.
        # Keep `command: - /manager`, `image:` and `imagePullPolicy:` on consecutive lines: the playbook
        # finds the toe container by exactly that sequence (ansible playbooks/vars/toe-manifest.yml toe_re).
        command:
        - /manager
        image: docker.io/tekn0ir/toe:toe_v4.2-4c366dae@sha256:c83116d0829561afc639eec90a75b82ee10e565b08e65dcb36e122a830935207
        imagePullPolicy: IfNotPresent
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8081
          initialDelaySeconds: 15
          periodSeconds: 20
        name: toe
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8081
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          limits:
            cpu: 500m
            memory: 512Mi
          requests:
            cpu: 10m
            memory: 64Mi
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
      serviceAccountName: teknoir-controller-manager
      terminationGracePeriodSeconds: 10
      volumes:
      - name: toe-volume
        hostPath:
          # directory location on host
          path: "/etc/teknoir"

EOL

info "Device bootstrapped successfully!"

info ""
info " _____ _____ _  ___   _  ___ ___ ____ "
info "|_   _| ____| |/ / \ | |/ _ \_ _|  _ \ "
info "  | | |  _| | ' /|  \| | | | | || |_) | "
info "  | | | |___| . \| |\  | |_| | ||  _ <  "
info "  |_| |_____|_|\_\_| \_|\___/___|_| \_\ "
info ""

info "Notice!"
info "No networking or user settings has been changed to match settings in the platform."

on_sudo() {
  $SUDO sh -- "$@"
}

setup_user() {
  $SUDO mkdir -p "/home/${_FIRST_USER_NAME}"
  if ! id -u "${_FIRST_USER_NAME}" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1 && command -v groupadd >/dev/null 2>&1; then
      if ! getent group "${_FIRST_USER_NAME}" >/dev/null 2>&1; then
        $SUDO groupadd "${_FIRST_USER_NAME}"
      fi
      $SUDO useradd \
        --gid "${_FIRST_USER_NAME}" \
        --groups users \
        --home-dir "/home/${_FIRST_USER_NAME}" \
        --shell /bin/bash \
        "${_FIRST_USER_NAME}"
    else
      $SUDO adduser --disabled-password --gecos "" "${_FIRST_USER_NAME}"
    fi
  fi

  if [ -z "${_FIRST_USER_PASS}" ]; then
    warn "No password for ${_FIRST_USER_NAME} in the platform: key-only login"
  elif command -v chpasswd &> /dev/null; then
    echo "${_FIRST_USER_NAME}:${_FIRST_USER_PASS}" | $SUDO chpasswd
  elif command -v passwd &> /dev/null; then
    echo -e "${_FIRST_USER_PASS}\n${_FIRST_USER_PASS}" | passwd "${_FIRST_USER_NAME}"
  elif command -v usermod &> /dev/null && command -v openssl &> /dev/null; then
    usermod --password $(openssl passwd -1 "${_FIRST_USER_PASS}") "${_FIRST_USER_NAME}"
  else
      warn "Could not set password for user ${_FIRST_USER_NAME}"
      info "Skipping the platform user setup"
      exit
  fi

  on_sudo << EOF
mkdir -p "/home/${_FIRST_USER_NAME}/.ssh"
cp "${CONFIG_PATH}/rsa_private.pem" "/home/${_FIRST_USER_NAME}/.ssh/id_rsa"
ssh-keygen -y -f "${CONFIG_PATH}/rsa_private.pem" > "/home/${_FIRST_USER_NAME}/.ssh/id_rsa.pub"
cat "/home/${_FIRST_USER_NAME}/.ssh/id_rsa.pub" > "/home/${_FIRST_USER_NAME}/.ssh/authorized_keys"
if [ -z "\${_FIRST_USER_KEY+x}" ] || [ "\${_FIRST_USER_KEY}" = "" ]; then
  echo "[INFO] No extra SSH key for the user in the platform; the device key authorizes logins";
else
  echo "\${_FIRST_USER_KEY}" >> "/home/${_FIRST_USER_NAME}/.ssh/authorized_keys"
fi
chmod 600 /home/${_FIRST_USER_NAME}/.ssh/*
chmod 744 /home/${_FIRST_USER_NAME}/.ssh
chown -R "${_FIRST_USER_NAME}:${_FIRST_USER_NAME}" "/home/${_FIRST_USER_NAME}"

# Add the user to the users and sudo groups (once: a re-run must not list it twice)
for _tn_group in users sudo; do
  if { getent group "\${_tn_group}" > /dev/null 2>&1 || grep -q "^\${_tn_group}:" /etc/group 2> /dev/null; } && ! id -nG "${_FIRST_USER_NAME}" | tr ' ' '\\n' | grep -qx "\${_tn_group}"; then
    if command -v usermod > /dev/null 2>&1; then
      usermod -aG "\${_tn_group}" "${_FIRST_USER_NAME}"
    else
      sed -i "/^\${_tn_group}:/s/\\(.*\\)/\\1,${_FIRST_USER_NAME}/;s/:,/:/" /etc/group
    fi
  fi
done
EOF

  setup_user_sudo
}

# A key-only platform user (no password in the platform) cannot answer sudo's
# password prompt, so automation over SSH (Ansible become, tnctl) could never
# act as root. Give exactly that user passwordless sudo with a validated
# drop-in. With a password set, the drop-in is removed again and sudo asks for
# the password. The rule names the user by uid (#<uid>), so no user name can
# turn into a sudoers keyword, and the file name has no '.' (sudo's includedir
# skips such files). Drop-ins of earlier platform users are removed.
setup_user_sudo() {
  _tn_sudoers="/etc/sudoers.d/90-teknoir-platform-user"
  # find, not a glob: the bootstrap scripts run with noglob
  if [ -d /etc/sudoers.d ]; then
    $SUDO find /etc/sudoers.d -maxdepth 1 -type f -name '90-teknoir-*' ! -name '90-teknoir-platform-user' -exec rm -f {} \;
  fi
  if [ -n "${_FIRST_USER_PASS}" ]; then
    $SUDO rm -f "${_tn_sudoers}"
    return 0
  fi
  if [ ! -d /etc/sudoers.d ]; then
    warn "No /etc/sudoers.d: ${_FIRST_USER_NAME} gets no passwordless sudo"
    return 0
  fi
  _tn_uid=$(id -u "${_FIRST_USER_NAME}" 2> /dev/null) || _tn_uid=""
  case "${_tn_uid}" in
    ''|*[!0-9]*|0)
      warn "No usable uid for ${_FIRST_USER_NAME}: no passwordless sudo"
      return 0
      ;;
  esac
  _tn_tmp=$($SUDO mktemp /etc/sudoers.d/.teknoir.XXXXXX) || { warn "Cannot create a sudoers drop-in: no passwordless sudo for ${_FIRST_USER_NAME}"; return 0; }
  if ! echo "#${_tn_uid} ALL=(ALL:ALL) NOPASSWD: ALL" | $SUDO tee "${_tn_tmp}" > /dev/null \
    || ! $SUDO chmod 0440 "${_tn_tmp}" \
    || { command -v visudo > /dev/null 2>&1 && ! $SUDO visudo -cf "${_tn_tmp}" > /dev/null; }; then
    $SUDO rm -f "${_tn_tmp}"
    warn "The sudoers drop-in for ${_FIRST_USER_NAME} could not be written or did not validate; no passwordless sudo"
    return 0
  fi
  $SUDO mv -f "${_tn_tmp}" "${_tn_sudoers}"
  info "Key-only login: ${_FIRST_USER_NAME} has passwordless sudo (${_tn_sudoers})"
}

setup_user

cd ..
info "Device bootstrapped successfully!"



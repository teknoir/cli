---
name: teknoir-platform
description: "Use when working with Teknoir devices, robots or teamspaces, or with the tnctl CLI: what the platform is and how to start safely."
license: Apache-2.0
metadata:
  teknoir-kit-version: "0.1.0"
---

# Teknoir platform

Teknoir operates Robots: edge computers (one, or a k3s cluster of several) that run workloads close to
the data and are managed from the platform, in teamspaces (one Kubernetes namespace each).

Start with `tnctl whoami`: it says which account or TeknoirBot this shell is and which teamspace it uses.

Never fetch or print device credentials, keys or passwords: the platform hands them out only to a person
with the rights for it, and only as that person needs them.

The full skills arrive in kit 0.2; this placeholder names the platform and the safe start.

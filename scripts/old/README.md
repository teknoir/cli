# Legacy Scripts

This directory contains legacy bash scripts for managing Teknoir resources. 
Note: These scripts are being replaced by the `tnctl` CLI tool.

### SSH to a Device:
```bash
ssh_device.sh -c gke_teknoir_us-central1-c_teknoir-cluster -n teknoir-retail -d orin-demo-se
```

### Port-Forward to MQTT Broker on a Device:
```bash
tunnel_device.sh -c gke_teknoir_us-central1-c_teknoir-cluster --namespace teknoir-ai --device orin-agx-64gb-se --port 31883 --to 127.0.0.1:31883
```
*Connect to the Device´s MQTT Broker on localhost:31883*

### Port-Forward to Devstudio on a Device:
```bash
tunnel_device.sh -c gke_teknoir_us-central1-c_teknoir-cluster --namespace teknoir-ai --device orin-agx-64gb-se --port 8080 --to 127.0.0.1:31880
```
*Browse to http://localhost:8080*

### Port-Forward to an IP-Camera´s Web interface on the same network as the Device:
```bash
tunnel_device.sh -c gke_teknoir_us-central1-c_teknoir-cluster --namespace teknoir-ai --device orin-agx-64gb-se --port 8080 --to 192.168.2.137:80
```
*Browse to http://localhost:8080*

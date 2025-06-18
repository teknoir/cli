#!/usr/bin/env bash
#set -e

# This script continuously syncs video segments from a device to a GCP bucket every hour.
while true; do
  for i in $(seq -w 0 28); do
    echo "Syncing video segments for iteration $(printf "%02d" $i)..."
    rsync_device.sh --context teknoir-prod --namespace victra-poc --device victra-poc-02 \
        --source /opt/teknoir/video/segments/ --destination /Volumes/VIDEOS/victra-poc-02/ \
        --pull --opts "--include='nc0009-front-door_0$(printf "%02d" $i)*.mp4' --exclude='*'"
    RSYNC_EXIT_CODE=$?

    if [[ $RSYNC_EXIT_CODE -ne 0 && $RSYNC_EXIT_CODE -ne 1 ]]; then
      echo "Rsync failed with exit code $RSYNC_EXIT_CODE. Continue..."
    fi

    gsutil -m rsync /Volumes/VIDEOS/victra-poc-02/ gs://victra-poc.teknoir.cloud/media/multiple-live-cameras/
    RSYNC_EXIT_CODE=$?

    if [[ $RSYNC_EXIT_CODE -ne 0 && $RSYNC_EXIT_CODE -ne 1 ]]; then
      echo "Rsync(gsutil) failed with exit code $RSYNC_EXIT_CODE. Continue..."
    fi

    rsync_device.sh --context teknoir-prod --namespace victra-poc --device victra-poc-03 \
        --source /opt/teknoir/video/segments/ --destination /Volumes/VIDEOS/victra-poc-03/ \
        --pull --opts "--include='nc0211-front-door-1_0$(printf "%02d" $i)*.mp4' --include='nc0211-front-door-2_0$(printf "%02d" $i)*.mp4' --exclude='*'"
    RSYNC_EXIT_CODE=$?

    if [[ $RSYNC_EXIT_CODE -ne 0 && $RSYNC_EXIT_CODE -ne 1 ]]; then
      echo "Rsync failed with exit code $RSYNC_EXIT_CODE. Continue..."
    fi

    gsutil -m rsync /Volumes/VIDEOS/victra-poc-03/ gs://victra-poc.teknoir.cloud/media/multiple-live-cameras/
    RSYNC_EXIT_CODE=$?

    if [[ $RSYNC_EXIT_CODE -ne 0 && $RSYNC_EXIT_CODE -ne 1 ]]; then
      echo "Rsync(gsutil) failed with exit code $RSYNC_EXIT_CODE. Continue..."
    fi

  done


  echo "Sync completed. Waiting for 1 hour before the next sync..."
  sleep 1h
done
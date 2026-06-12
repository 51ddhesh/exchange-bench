#/usr/bin/env bash
for repo in api worker ingester leaderboard compiler runner contestant; do
  aws ecr batch-delete-image \
    --region ap-south-1 \
    --repository-name exchange-bench-${repo} \
    --image-ids "$(aws ecr list-images --region ap-south-1 --repository-name exchange-bench-${repo} --query 'imageIds[*]' --output json)" \
    2>/dev/null || true
done

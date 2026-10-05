#!/bin/sh
set -e

echo "============================================="
echo "   community-scripts Telemetry Service"
echo "============================================="

echo "🚀 Starting telemetry service..."
exec /app/telemetry-service

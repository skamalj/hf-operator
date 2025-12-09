#!/bin/bash
set -e

TOKEN="$1"
MODEL_NAME="$2"
ENABLE_TRANSFER="$3"
LFS_BATCH_SIZE="${4:-50}"       # Default batch size 50 if not provided
MODEL_DIR="/models/$MODEL_NAME"

echo "====================================="
echo "[INFO] Starting Git LFS download"
echo "Model:        $MODEL_NAME"
echo "Token set:    yes"
echo "Batch size:   $LFS_BATCH_SIZE"
echo "Target dir:   $MODEL_DIR"
echo "====================================="

# If model folder exists and has content, skip
if [ -d "$MODEL_DIR" ] && [ "$(ls -A $MODEL_DIR 2>/dev/null | wc -l)" -gt 0 ]; then
    echo "[INFO] Model already exists → skipping download."
    exit 0
fi

mkdir -p "$MODEL_DIR"
cd "$MODEL_DIR"

echo "[INFO] Installing required packages..."
apt-get update -y && apt-get install -y git git-lfs python3 python3-pip curl
git lfs install

echo "[INFO] Cloning HuggingFace repo using Git LFS..."
GIT_URL="https://huggingface.co/${MODEL_NAME}"

# Use token inline to authenticate
git clone "https://${TOKEN}@${GIT_URL#https://}" .

echo "[INFO] Configuring Git LFS batch size..."
git config lfs.concurrenttransfers "$LFS_BATCH_SIZE"

echo "[INFO] Fetching LFS objects (this may take a long time)..."
git lfs fetch --include="*" --exclude="" --verbose

echo "[INFO] Pulling LFS objects..."
git lfs pull --include="*" --exclude="" --verbose

echo "[INFO] Download complete!"
du -sh "$MODEL_DIR"

echo "====================================="
echo "[SUCCESS] Model downloaded via Git LFS"
echo "====================================="

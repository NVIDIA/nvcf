#!/bin/bash

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Timestamp format
timestamp() {
    date '+%Y-%m-%d %H:%M:%S'
}

# Log levels with emojis
log_info() {
    echo -e "${GREEN}[INFO]${NC} ℹ️  $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} ⚠️  $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} ❌ $1"
}

log_step() {
    echo -e "${BLUE}[STEP]${NC} 🚀 $1"
}

log_success() {
    echo -e "${GREEN}[DONE]${NC} ✅ $1"
}

# Section separator (no timestamp)
log_section() {
    echo -e "\n${BLUE}=================================${NC}"
    echo -e "${BLUE}    $1${NC}"
    echo -e "${BLUE}=================================${NC}\n"
}
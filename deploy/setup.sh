#!/bin/bash
set -e

echo "Setting up ARM-optimized Envedour Bot..."

# Install ARM-optimized dependencies
sudo apt-get update
sudo apt-get install -y \
    ffmpeg \
    aria2 \
    python3 \
    python3-pip \
    redis-server \
    build-essential \
    ca-certificates \
    curl \
    unzip \
    wget

# Install Deno so yt-dlp can solve YouTube JavaScript challenges. yt-dlp
# currently requires Deno >= 2.3.0, so upgrade older installations too.
DENO_VERSION=""
if command -v deno >/dev/null 2>&1; then
    DENO_VERSION="$(deno --version 2>/dev/null | head -n1 | awk '{print $2}')"
fi
if [ -z "$DENO_VERSION" ] || [ "$(printf '%s\n' 2.3.0 "$DENO_VERSION" | sort -V | head -n1)" != "2.3.0" ]; then
    curl -fsSL https://deno.land/install.sh -o /tmp/deno-install.sh
    sudo env DENO_INSTALL=/usr/local sh /tmp/deno-install.sh
    rm -f /tmp/deno-install.sh
fi

# Install yt-dlp with curl-cffi support for TikTok impersonation. Recent
# Debian/Ubuntu releases mark the system Python as externally managed.
if ! sudo pip3 install --upgrade "yt-dlp[default,curl-cffi]"; then
    sudo pip3 install --upgrade --break-system-packages "yt-dlp[default,curl-cffi]"
fi

# Verify that the two pieces yt-dlp needs for YouTube actually work. Without a
# JavaScript runtime YouTube may return no playable formats or only images.
if ! command -v deno >/dev/null 2>&1; then
    echo "ERROR: Deno was not installed or is not in PATH." >&2
    echo "Install it manually: curl -fsSL https://deno.land/install.sh | sudo env DENO_INSTALL=/usr/local sh" >&2
    exit 1
fi
deno --version

if ! command -v yt-dlp >/dev/null 2>&1; then
    echo "ERROR: yt-dlp is not in PATH." >&2
    exit 1
fi
yt-dlp --version
if ! yt-dlp --help 2>/dev/null | grep -q -- '--js-runtimes'; then
    echo "WARNING: this yt-dlp is too old and does not support --js-runtimes." >&2
    echo "Upgrade it with: sudo pip3 install --upgrade 'yt-dlp[default,curl-cffi]'" >&2
fi

# Enable hardware acceleration (only if running as non-root user)
if [ "$EUID" -ne 0 ] && [ -n "$SUDO_USER" ]; then
    sudo usermod -a -G video "$SUDO_USER"
elif [ "$EUID" -ne 0 ]; then
    sudo usermod -a -G video "$USER"
fi

# Create tmpfs directory
sudo mkdir -p /dev/shm/videos
sudo chmod 1777 /dev/shm/videos

# Setup Redis
sudo systemctl enable redis-server
sudo systemctl start redis-server

# Configure Redis for ARM
# Create directory if it doesn't exist
sudo mkdir -p /etc/redis/redis.conf.d

sudo tee /etc/redis/redis.conf.d/arm-optimized.conf > /dev/null <<EOF
maxmemory 1gb
maxmemory-policy allkeys-lru
save ""
activerehashing yes
EOF

sudo systemctl restart redis-server

echo "Setup complete!"

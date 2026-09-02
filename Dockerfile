FROM debian:bookworm-slim

# Build args for host user mapping
ARG HOST_UID=1000
ARG HOST_GID=1000
ARG USERNAME=agent

# Install dependencies
RUN apt-get update && apt-get install -y \
    git \
    curl \
    wget \
    vim \
    jq \
    ripgrep \
    fd-find \
    fzf \
    build-essential \
    sudo \
    && rm -rf /var/lib/apt/lists/*

# Create a non-root user that files will be owned by on the host
# This user has sudo access so they can install things inside the container
RUN groupadd -g ${HOST_GID} ${USERNAME} \
    && useradd -m -u ${HOST_UID} -g ${HOST_GID} ${USERNAME} \
    && echo "${USERNAME} ALL=(ALL) NOPASSWD: ALL" >> /etc/sudoers

# Copy agent binary from dist/
COPY dist/aadc /usr/local/bin/aadc

USER root
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/aadc"]

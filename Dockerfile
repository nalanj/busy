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

# Install mise so the container can manage Go the same way the host does.
# MISE_INSTALL_PATH redirects from the default ~/.local/bin/mise.
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh

# Copy agent binary from dist/
COPY dist/busy /usr/local/bin/busy

# Pre-install Go for the agent user via mise so `go test`, `go build`,
# etc. are available when the LLM's bash tool runs them. The busy repo's
# mise.toml pins `go = "latest"`, so this also keeps the container in
# sync with the host's mise-managed Go.
USER ${USERNAME}
RUN /usr/local/bin/mise use --global go@latest && /usr/local/bin/mise install

# Make mise-managed binaries (go, gofmt, ...) on PATH for processes
# spawned by the busy binary, including the LLM's bash tool.
ENV PATH=/home/agent/.local/share/mise/shims:$PATH

WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/busy"]

# End-to-end CLI smoke test, modelling "install ao on a fresh machine, then use it".
#
# Build context is the REPO ROOT:
#   docker build -f test/cli/Dockerfile -t ao-cli-smoke .
#   docker run --rm --init ao-cli-smoke
#
# --init gives the container a real PID-1 reaper (tini) so a stopped daemon is
# reaped promptly; the test is written to not depend on it, but it keeps process
# accounting clean.

# ---- stage 1: build the binary (the "release" a user would download) ----
FROM golang:1.25-bookworm AS build
WORKDIR /src

# Cache modules first.
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download

COPY backend ./backend
# Pure-Go SQLite (modernc) builds fine with CGO disabled -> a static binary.
RUN cd backend && CGO_ENABLED=0 go build -trimpath -o /out/ao ./cmd/ao

# ---- stage 2: a clean machine with NO Go toolchain, just like an end user ----
FROM debian:bookworm-slim AS run

# Runtime deps a fresh user would need: git is required by `ao doctor`; tmux is
# the optional runtime it probes for; curl drives the HTTP-level guard checks;
# ca-certificates for good measure.
RUN apt-get update \
 && apt-get install -y --no-install-recommends git tmux curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# "Install" the CLI the way a user would: drop the binary on PATH.
COPY --from=build /out/ao /usr/local/bin/ao
COPY test/cli/smoke.sh /usr/local/bin/ao-smoke.sh
RUN chmod +x /usr/local/bin/ao /usr/local/bin/ao-smoke.sh

# Run as an unprivileged user with a real HOME, like a normal install.
RUN useradd --create-home --shell /bin/bash ao
USER ao
WORKDIR /home/ao

# Sanity: prove the install resolved before the suite runs.
RUN ao version

ENTRYPOINT ["/usr/local/bin/ao-smoke.sh"]

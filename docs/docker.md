# Docker Deploy

TYA is published as a multi-arch Docker image to the GitHub Container Registry (GHCR) on every versioned tag and release.

## Image

```
ghcr.io/vhpedrogithub/tya:<version>
ghcr.io/vhpedrogithub/tya:latest        # points to the latest release
```

Available platforms: `linux/amd64`, `linux/arm64`.

---

## Quick Start

```bash
# Pull the latest release
docker pull ghcr.io/vhpedrogithub/tya:latest

# Print help
docker run --rm ghcr.io/vhpedrogithub/tya:latest

# Run against a local API
docker run --rm \
  -v $(pwd):/workspace \
  -e TYA_BASE_URL=http://host.docker.internal:8080 \
  ghcr.io/vhpedrogithub/tya:latest run -t
```

> On Linux replace `host.docker.internal` with the host's IP (e.g. `172.17.0.1`) or use `--network host`.

---

## Mounting a Project Directory

TYA expects your project files (`config-run.yml`, `api/`, `models/`) to exist in the working directory. Mount your project folder as `/workspace`:

```bash
docker run --rm \
  -v /path/to/my-project:/workspace \
  -e TYA_BASE_URL=http://api.example.com \
  ghcr.io/vhpedrogithub/tya:latest run
```

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `TYA_BASE_URL` | Base URL of the API under test (e.g. `http://api.example.com`) |

All other secrets (auth credentials) are referenced via `${ENV_VAR}` in `config-run.yml` and passed in with `-e`:

```bash
docker run --rm \
  -v $(pwd):/workspace \
  -e TYA_BASE_URL=https://api.example.com \
  -e TEST_USER=alice@example.com \
  -e TEST_PASS=s3cr3t \
  ghcr.io/vhpedrogithub/tya:latest run
```

---

## Docker Compose Example

```yaml
services:
  app:
    image: your-api-image
    ports:
      - "8080:8080"

  tya:
    image: ghcr.io/vhpedrogithub/tya:latest
    volumes:
      - ./tya-project:/workspace
    environment:
      TYA_BASE_URL: http://app:8080
      TEST_USER: alice@example.com
      TEST_PASS: s3cr3t
    command: run -t
    depends_on:
      - app
```

---

## Building the Image Locally

```bash
docker build -t tya:local .

docker run --rm \
  -v $(pwd):/workspace \
  -e TYA_BASE_URL=http://host.docker.internal:8080 \
  tya:local run -t
```

---

## Image Lifecycle

The Docker image is built and pushed automatically by the `docker.yml` GitHub Actions workflow:

| Trigger | Tags pushed |
|---------|-------------|
| Push of tag `x.y.z` | `x.y.z`, `x.y`, `x` |
| Publish a GitHub Release | `x.y.z`, `x.y`, `x`, `latest` |

Images are published to `ghcr.io/vhpedrogithub/tya`. No credentials are required to pull public images.

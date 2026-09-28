# syntax=docker/dockerfile:1
ARG PYTHON_BASE
FROM ${PYTHON_BASE}

ARG WOW_SOURCE_REVISION

RUN test -n "$WOW_SOURCE_REVISION" \
    && case "$WOW_SOURCE_REVISION" in (*[!0-9a-f]*|'') exit 2;; esac \
    && test "${#WOW_SOURCE_REVISION}" -eq 40

WORKDIR /opt/wow-sidecar-build
COPY pyproject.toml ./
COPY src ./src
RUN python -m pip install --disable-pip-version-check --no-cache-dir . \
    && install -d -m 0755 /usr/share/wow-sidecar \
    && printf '%s\n' "$WOW_SOURCE_REVISION" > /usr/share/wow-sidecar/source-revision \
    && chmod 0444 /usr/share/wow-sidecar/source-revision \
    && useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin wow-sidecar

WORKDIR /var/lib/wow-sidecar
USER wow-sidecar:wow-sidecar

ENTRYPOINT ["wow-sidecar-worker"]

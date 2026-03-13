FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        coreutils \
        findutils \
        bash \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

CMD ["sleep", "infinity"]

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -o /out/klovys99 ./cmd/klovys99

FROM golang:1.25-bookworm

ARG CLAUDE_CODE_NPM_PACKAGE=@anthropic-ai/claude-code

ENV DEBIAN_FRONTEND=noninteractive \
    HOME=/home/klovys \
    KLOVIS_ADDR=0.0.0.0:8080 \
    KLOVIS_GLINER_MODE=full \
    KLOVIS_GLINER_URL=http://gliner:8091 \
    KLOVYS99_AI_WORKSPACE_DIR=/data/ai-workspace

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl nodejs npm util-linux \
    && npm install -g "${CLAUDE_CODE_NPM_PACKAGE}" \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --uid 10001 klovys \
    && mkdir -p /data/ai-workspace /home/klovys/.claude \
    && chown -R klovys:klovys /data /home/klovys

COPY --from=build /out/klovys99 /usr/local/bin/klovys99

USER klovys
WORKDIR /workspace

VOLUME ["/data/ai-workspace", "/home/klovys/.claude"]

EXPOSE 8080

CMD ["klovys99"]

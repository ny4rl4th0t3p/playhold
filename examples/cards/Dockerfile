# Build from the repo root, which holds everything these images need:
#   docker build -f examples/cards/Dockerfile --target server .

FROM node:24-alpine AS page
WORKDIR /src
COPY sdk/ts/package.json sdk/ts/package-lock.json sdk/ts/
COPY examples/cards/web/package.json examples/cards/web/package-lock.json examples/cards/web/
RUN npm --prefix sdk/ts ci && npm --prefix examples/cards/web ci
COPY sdk/ts sdk/ts
COPY examples/cards/web examples/cards/web
RUN npm --prefix examples/cards/web run build

FROM golang:1.27.4-alpine AS binaries
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -o /out/server ./examples/cards/server
RUN go build -trimpath -o /out/bot ./examples/cards/bot
# The crash journal's folder. A new volume mounted there takes this owner, so the server can write to it.
RUN mkdir -p /out/journal

FROM scratch AS server
COPY --from=binaries /out/server /server
COPY --from=page /src/examples/cards/web/dist /web
COPY --from=binaries --chown=65534:65534 /out/journal /var/lib/cards
USER 65534
EXPOSE 8080
ENTRYPOINT ["/server", "-web", "/web"]

FROM scratch AS bot
COPY --from=binaries /out/bot /bot
USER 65534
ENTRYPOINT ["/bot"]
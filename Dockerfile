FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
ARG TARGETOS
ARG TARGETARCH
ARG COMPONENT
WORKDIR /
COPY bin/$COMPONENT.$TARGETOS-$TARGETARCH /<component>
# USER 65532:65532

# docker doesn't substitue args in ENTRYPOINT, so we replace this during the build script
ENTRYPOINT ["/<component>"]

# The image of mimic, the test identity provider of development (ADR 0054). It is a test tool and not part of a release:
# .github/workflows/mimic.yaml builds it on a push to main, from the executables its build job made, and pushes it as
# ghcr.io/truvity/sluis-mimic:main-<commit>. The build context holds <os>/<arch>/sluis-mimic for each platform.
# Nothing here runs a build: the privileged job only copies.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG TARGETPLATFORM
LABEL org.opencontainers.image.title="sluis mimic" \
      org.opencontainers.image.description="mimic, a test identity provider for development (ADR 0054); refuses to start outside devel" \
      org.opencontainers.image.source="https://github.com/truvity/sluis" \
      org.opencontainers.image.licenses="MIT"
COPY --chmod=0555 ${TARGETPLATFORM}/sluis-mimic /usr/bin/sluis-mimic
USER 65532:65532
ENTRYPOINT ["/usr/bin/sluis-mimic"]
CMD ["serve", "-config", "/etc/mimic/mimic.yaml"]

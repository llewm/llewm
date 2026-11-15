# Build the static binary and deployment bundle with ../build-bundle.sh first.
# The context is that bundle, never the source checkout or a credential directory.
FROM scratch
COPY --chmod=0555 llewm-orchestrate /usr/local/bin/llewm-orchestrate
COPY --chown=10001:10001 --chmod=0700 data /data
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/llewm-orchestrate"]
CMD ["shared-broker-serve", "--db", "/data/messages.db", "--listen", "127.0.0.1:9469", "--proxy-loopback"]

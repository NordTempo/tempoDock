# Uses ~3 GB during the build, over the 2 GB BuildKit cap. The context is
# copied first so a fresh nonce file (see e2e/run.sh) defeats the build cache.
FROM python:3.13-alpine
COPY . /ctx
RUN python3 -c 'b = b"x" * (3 << 30); print(len(b))'
CMD ["python3", "-m", "http.server", "8080"]

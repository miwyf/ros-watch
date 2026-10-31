FROM python:3.13-slim-bookworm AS runtime
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 DATA_DIR=/data
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt \
    && groupadd --gid 10001 roswatch \
    && useradd --uid 10001 --gid 10001 --no-create-home roswatch \
    && mkdir /data && chown roswatch:roswatch /data
COPY --chown=10001:10001 roswatch ./roswatch
USER 10001:10001
EXPOSE 8080/tcp 5514/udp
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/healthz',timeout=3)"
CMD ["python", "-m", "roswatch"]

# Optional verification target; test dependencies are absent from production.
FROM runtime AS test
USER root
RUN pip install --no-cache-dir pytest==9.2.0
COPY tests ./tests
COPY pytest.ini .
USER 10001:10001
RUN python -m pytest -q -p no:cacheprovider

FROM runtime AS production
